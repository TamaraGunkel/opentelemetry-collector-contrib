// Copyright 2021, OpenTelemetry Authors
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
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/DataDog/datadog-agent/pkg/trace/exportable/pb"
	"github.com/tinylib/msgp/msgp"
	"go.opentelemetry.io/collector/consumer/pdata"
	tracetranslator "go.opentelemetry.io/collector/translator/trace"
	"mime"
	"net/http"
	"strings"
)

func ToTraces(traces pb.Traces, req *http.Request) pdata.Traces {
	dest := pdata.NewTraces()
	ils := dest.ResourceSpans().AppendEmpty().InstrumentationLibrarySpans().AppendEmpty()

	// TODO: Pull from HTTP headers ils.InstrumentationLibrary().SetName()
	// TODO: Pull from HTTP headers ils.InstrumentationLibrary().SetVersion()
	for _, trace := range traces {
		for _, span := range trace {
			newSpan := ils.Spans().AppendEmpty() // TODO: Might be more efficient to resize spans and then populate it

			buf := make([]byte, 16)
			binary.PutUvarint(buf, span.TraceID)
			var traceId [16]byte
			copy(traceId[:], buf)
			// TODO  Verify this is making correct translations
			newSpan.SetTraceID(pdata.NewTraceID(traceId))
			newSpan.SetSpanID(tracetranslator.UInt64ToSpanID(span.SpanID))
			newSpan.SetStartTimestamp(pdata.Timestamp(span.Start))
			newSpan.SetEndTimestamp(pdata.Timestamp(span.Start + span.Duration))
			newSpan.SetParentSpanID(tracetranslator.UInt64ToSpanID(span.ParentID))
			newSpan.SetName(span.Name)
			// TODO: Figure out tag to pull from newSpan.Status().SetCode()
			for k, v := range span.GetMeta() {
				newSpan.Attributes().InsertString(k, v)
			}

			switch span.Type {
			case "web":
				newSpan.SetKind(pdata.SpanKindSERVER)
			case "client":
				newSpan.SetKind(pdata.SpanKindCLIENT)
			default:
				newSpan.SetKind(pdata.SpanKindUNSPECIFIED)
			}

		}
	}
	return dest
}

func decodeRequest(req *http.Request, dest *pb.Traces) error {
	switch mediaType := getMediaType(req); mediaType {
	case "application/msgpack":
		if strings.Contains(req.RequestURI, "v0.5") {
			reader := pb.NewMsgpReader(req.Body)
			defer pb.FreeMsgpReader(reader)
			return dest.DecodeMsgDictionary(reader)
		} else {
			return msgp.Decode(req.Body, dest)
		}
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
				reader := pb.NewMsgpReader(req.Body)
				defer pb.FreeMsgpReader(reader)
				if err3 := dest.DecodeMsgDictionary(reader); err3 != nil {
					return fmt.Errorf("could not decode JSON (%q), nor Msgpack (%q), nor v0.5 (%q)", err1, err2, err3)
				}
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
