// Copyright 2019, OpenTelemetry Authors
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
	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/config"
	"go.opentelemetry.io/collector/config/confighttp"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.uber.org/zap"
	"testing"
)

func TestDatadogReceiver_Lifecycle(t *testing.T) {

	_, err := newDataDogReceiver(&Config{
		ReceiverSettings: config.NewReceiverSettings(config.NewID("datadog")),
		HTTPServerSettings: confighttp.HTTPServerSettings{
			Endpoint: "0.0.0.0:8126",
		},
	}, consumertest.NewNop(), component.ReceiverCreateParams{Logger: zap.NewNop()})

	assert.NoError(t, err, "Receiver should be created")

}
