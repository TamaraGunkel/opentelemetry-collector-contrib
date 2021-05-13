// Copyright 2020, OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package datadogreceiver

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/DataDog/datadog-agent/pkg/trace/exportable/pb"
	"github.com/gorilla/mux"
	"github.com/philhofer/fwd"
	"github.com/tinylib/msgp/msgp"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/component/componenterror"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/pdata"
	"go.opentelemetry.io/collector/obsreport"
	tracetranslator "go.opentelemetry.io/collector/translator/trace"
	"io"
	"mime"
	"net/http"
	"sync"
)

type datadogReceiver struct {
	config       *Config
	params       component.ReceiverCreateParams
	nextConsumer consumer.Traces
	server       *http.Server
	longLivedCtx context.Context

	mu         sync.Mutex
	startOnce  sync.Once
	stopOnce   sync.Once
	readerPool sync.Pool
}

func newDataDogReceiver(config *Config, nextConsumer consumer.Traces, params component.ReceiverCreateParams) (component.TracesReceiver, error) {
	if nextConsumer == nil {
		return nil, componenterror.ErrNilNextConsumer
	}
	return &datadogReceiver{
		params:       params,
		config:       config,
		nextConsumer: nextConsumer,
		readerPool:   sync.Pool{New: func() interface{} { return &msgp.Reader{} }},
	}, nil
}

const collectorHTTPTransport = "http_collector"

func (ddr *datadogReceiver) Start(ctx context.Context, host component.Host) error {
	var err error
	ddr.longLivedCtx = obsreport.ReceiverContext(ctx, "datadog", "http")
	ddr.startOnce.Do(func() {
		ddmux := mux.NewRouter()
		ddmux.HandleFunc("/v0.3/traces", ddr.handleTraces)
		ddmux.HandleFunc("/v0.4/traces", ddr.handleTraces)
		ddmux.HandleFunc("/v0.5/traces", ddr.handleTraces05)
		ddr.server = &http.Server{
			Handler: ddmux,
			Addr:    ddr.config.HTTPServerSettings.Endpoint,
		}
		if err := ddr.server.ListenAndServe(); err != http.ErrServerClosed {
			host.ReportFatalError(fmt.Errorf("error starting datadog receiver: %v", err))
		}
	})
	return err
}

func (ddr *datadogReceiver) Shutdown(ctx context.Context) error {
	ddr.stopOnce.Do(func() {
		_ = ddr.server.Shutdown(context.Background())
	})
	return nil
}

func (ddr *datadogReceiver) handleTraces(w http.ResponseWriter, req *http.Request) {
	obsreport.StartTraceDataReceiveOp(ddr.longLivedCtx, typeStr, "http")
	var traces pb.Traces
	err := decodeRequest(req, &traces)
	if err != nil {
		http.Error(w, "Unable to unmarshal reqs", http.StatusInternalServerError)
		obsreport.EndTraceDataReceiveOp(ddr.longLivedCtx, typeStr, 0, err)
		return
	}

	ddr.processTraces(req.Context(), traces, w)
}

func (ddr *datadogReceiver) handleTraces05(w http.ResponseWriter, req *http.Request) {
	obsreport.StartTraceDataReceiveOp(ddr.longLivedCtx, typeStr, collectorHTTPTransport)
	traces := pb.Traces{}
	reader := ddr.NewMsgpReader(req.Body)
	err := traces.DecodeMsgDictionary(reader)
	ddr.FreeMsgpReader(reader)
	if err != nil {
		http.Error(w, "Unable to unmarshal reqs", http.StatusInternalServerError)
		obsreport.EndTraceDataReceiveOp(ddr.longLivedCtx, typeStr, 0, err)
		return
	}
	ddr.processTraces(req.Context(), traces, w)
}

func (ddr *datadogReceiver) processTraces(ctx context.Context, traces pb.Traces, w http.ResponseWriter) {
	newTraces := pdata.NewTraces()
	for _, trace := range traces {
		for _, span := range trace {
			newSpan := newTraces.ResourceSpans().AppendEmpty().InstrumentationLibrarySpans().AppendEmpty().Spans().AppendEmpty()
			newSpan.SetTraceID(tracetranslator.UInt64ToTraceID(span.TraceID, span.TraceID))
			newSpan.SetSpanID(tracetranslator.UInt64ToSpanID(span.SpanID))
			newSpan.SetStartTimestamp(pdata.Timestamp(span.Start))
			newSpan.SetEndTimestamp(pdata.Timestamp(span.Start + span.Duration))
			newSpan.SetParentSpanID(tracetranslator.UInt64ToSpanID(span.ParentID))
			newSpan.SetName(span.Name)
			for k, v := range span.GetMeta() {
				newSpan.Attributes().InsertString(k, v)
			}
			if span.Type == "web" {
				newSpan.SetKind(pdata.SpanKindSERVER)
			} else if span.Type == "client" {
				newSpan.SetKind(pdata.SpanKindCLIENT)
			} else {
				newSpan.SetKind(pdata.SpanKindINTERNAL)
			}
		}
	}

	err := ddr.nextConsumer.ConsumeTraces(ctx, newTraces)
	if err != nil {
		http.Error(w, "Trace consumer errored out", http.StatusInternalServerError)
	} else {
		_, _ = w.Write([]byte("OK"))
	}
	obsreport.EndTraceDataReceiveOp(ddr.longLivedCtx, typeStr, newTraces.SpanCount(), err)
}

/// Thanks Datadog!
func decodeRequest(req *http.Request, dest *pb.Traces) error {
	switch mediaType := getMediaType(req); mediaType {
	case "application/msgpack":
		return msgp.Decode(req.Body, dest)
	case "application/json":
		fallthrough
	case "text/json":
		fallthrough
	case "":
		return json.NewDecoder(req.Body).Decode(dest)
	default:
		// do our best
		if err1 := json.NewDecoder(req.Body).Decode(dest); err1 != nil {
			if err2 := msgp.Decode(req.Body, dest); err2 != nil {
				return fmt.Errorf("could not decode JSON (%q), nor Msgpack (%q)", err1, err2)
			}
		}
		return nil
	}
}

func getMediaType(req *http.Request) string {
	mt, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil {
		return "application/json"
	}
	return mt
}

func (ddr *datadogReceiver) NewMsgpReader(r io.Reader) *msgp.Reader {
	p := ddr.readerPool.Get().(*msgp.Reader)
	if p.R == nil {
		p.R = fwd.NewReader(r)
	} else {
		p.R.Reset(r)
	}
	return p
}

// FreeMsgpReader marks reader r as done.
func (ddr *datadogReceiver) FreeMsgpReader(r *msgp.Reader) { ddr.readerPool.Put(r) }
