// Copyright 2022 Buf Technologies, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package interopconnect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/bufbuild/connect-crosstest/internal/crosstesting"
	connectpb "github.com/bufbuild/connect-crosstest/internal/gen/proto/connect/grpc/testing/testingconnect"
	testpb "github.com/bufbuild/connect-crosstest/internal/gen/proto/go/grpc/testing"
	"github.com/bufbuild/connect-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/benchmark/stats"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

const (
	eightBytes          = 8
	sixteenBytes        = 16
	oneKiB              = 1024
	twoKiB              = 2028
	thirtyTwoKiB        = 32768
	sixtyFourKiB        = 65536
	twoFiftyKiB         = 256000
	fiveHundredKiB      = 512000
	largeReqSize        = twoFiftyKiB
	largeRespSize       = fiveHundredKiB
	initialMetadataKey  = "x-grpc-test-echo-initial"
	trailingMetadataKey = "x-grpc-test-echo-trailing-bin"
)

var (
	reqSizes  = []int{twoFiftyKiB, eightBytes, oneKiB, thirtyTwoKiB}
	respSizes = []int{fiveHundredKiB, sixteenBytes, twoKiB, sixtyFourKiB}
)

// ClientNewPayload returns a payload of the given type and size.
func ClientNewPayload(t crosstesting.TB, payloadType testpb.PayloadType, size int) (*testpb.Payload, error) {
	t.Helper()
	if size < 0 {
		return nil, fmt.Errorf("Requested a response with invalid length %d", size)
	}
	body := make([]byte, size)
	assert.Equal(t, payloadType, testpb.PayloadType_COMPRESSABLE)
	return &testpb.Payload{
		Type: payloadType,
		Body: body,
	}, nil
}

// DoEmptyUnaryCall performs a unary RPC with empty request and response messages.
func DoEmptyUnaryCall(t crosstesting.TB, client connectpb.TestServiceClient) {
	reply, err := client.EmptyCall(
		context.Background(),
		connect.NewRequest(&testpb.Empty{}),
	)
	require.NoError(t, err)
	assert.True(t, proto.Equal(&testpb.Empty{}, reply.Msg))
	t.Successf("succcessful unary call")
}

// DoLargeUnaryCall performs a unary RPC with large payload in the request and response.
func DoLargeUnaryCall(t crosstesting.TB, client connectpb.TestServiceClient) {
	pl, err := ClientNewPayload(t, testpb.PayloadType_COMPRESSABLE, largeReqSize)
	require.NoError(t, err)
	req := &testpb.SimpleRequest{
		ResponseType: testpb.PayloadType_COMPRESSABLE,
		ResponseSize: int32(largeRespSize),
		Payload:      pl,
	}
	reply, err := client.UnaryCall(context.Background(), connect.NewRequest(req))
	require.NoError(t, err)
	assert.Equal(t, reply.Msg.GetPayload().GetType(), testpb.PayloadType_COMPRESSABLE)
	assert.Equal(t, len(reply.Msg.GetPayload().GetBody()), largeRespSize)
	t.Successf("successful large unary call")
}

// DoClientStreaming performs a client streaming RPC.
func DoClientStreaming(t crosstesting.TB, client connectpb.TestServiceClient) {
	stream := client.StreamingInputCall(context.Background())
	var sum int
	for _, size := range reqSizes {
		pl, err := ClientNewPayload(t, testpb.PayloadType_COMPRESSABLE, size)
		require.NoError(t, err)
		req := &testpb.StreamingInputCallRequest{
			Payload: pl,
		}
		require.NoError(t, stream.Send(req))
		sum += size
	}
	reply, err := stream.CloseAndReceive()
	require.NoError(t, err)
	assert.Equal(t, reply.Msg.GetAggregatedPayloadSize(), int32(sum))
	t.Successf("successful client streaming test")
}

// DoServerStreaming performs a server streaming RPC.
func DoServerStreaming(t crosstesting.TB, client connectpb.TestServiceClient) {
	respParam := make([]*testpb.ResponseParameters, len(respSizes))
	for i, s := range respSizes {
		respParam[i] = &testpb.ResponseParameters{
			Size: int32(s),
		}
	}
	req := &testpb.StreamingOutputCallRequest{
		ResponseType:       testpb.PayloadType_COMPRESSABLE,
		ResponseParameters: respParam,
	}
	stream, err := client.StreamingOutputCall(context.Background(), connect.NewRequest(req))
	require.NoError(t, err)
	var respCnt int
	var index int
	for stream.Receive() {
		assert.Equal(t, stream.Msg().GetPayload().GetType(), testpb.PayloadType_COMPRESSABLE)
		assert.Equal(t, len(stream.Msg().GetPayload().GetBody()), respSizes[index])
		index++
		respCnt++
	}
	require.NoError(t, stream.Err())
	assert.Equal(t, respCnt, len(respSizes))
	t.Successf("successful server streaming test")
}

// DoPingPong performs ping-pong style bi-directional streaming RPC.
func DoPingPong(t crosstesting.TB, client connectpb.TestServiceClient) {
	stream := client.FullDuplexCall(context.Background())
	assert.NotNil(t, stream)
	var index int
	for index < len(reqSizes) {
		respParam := []*testpb.ResponseParameters{
			{
				Size: int32(respSizes[index]),
			},
		}
		pl, err := ClientNewPayload(t, testpb.PayloadType_COMPRESSABLE, reqSizes[index])
		require.NoError(t, err)
		req := &testpb.StreamingOutputCallRequest{
			ResponseType:       testpb.PayloadType_COMPRESSABLE,
			ResponseParameters: respParam,
			Payload:            pl,
		}
		require.NoError(t, stream.Send(req))
		reply, err := stream.Receive()
		require.NoError(t, err)
		assert.Equal(t, reply.GetPayload().GetType(), testpb.PayloadType_COMPRESSABLE)
		assert.Equal(t, len(reply.GetPayload().GetBody()), respSizes[index])
		index++
	}
	require.NoError(t, stream.CloseSend())
	_, err := stream.Receive()
	assert.True(t, errors.Is(err, io.EOF))
	t.Successf("successful ping pong")
}

// DoEmptyStream sets up a bi-directional streaming with zero message.
func DoEmptyStream(t crosstesting.TB, client connectpb.TestServiceClient) {
	stream := client.FullDuplexCall(context.Background())
	assert.NotNil(t, stream)
	require.NoError(t, stream.CloseSend())
	_, err := stream.Receive()
	assert.Error(t, err)
	assert.True(t, errors.Is(err, io.EOF))
	t.Successf("successful empty stream")
}

// DoTimeoutOnSleepingServer performs an RPC on a sleep server which causes RPC timeout.
func DoTimeoutOnSleepingServer(t crosstesting.TB, client connectpb.TestServiceClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	stream := client.FullDuplexCall(ctx)
	assert.NotNil(t, stream)
	pl, err := ClientNewPayload(t, testpb.PayloadType_COMPRESSABLE, 27182)
	require.NoError(t, err)
	req := &testpb.StreamingOutputCallRequest{
		ResponseType: testpb.PayloadType_COMPRESSABLE,
		Payload:      pl,
	}
	err = stream.Send(req)
	if err != nil {
		// This emulates the gRPC test case, where due to network issues,
		// the stream has already timed out before the `Send` and so this would
		// return a EOF.
		assert.True(t, errors.Is(err, io.EOF) || connect.CodeOf(err) == connect.CodeDeadlineExceeded)
		t.Successf("successful timeout on sleep")
		return
	}
	require.NoError(t, err)
	_, err = stream.Receive()
	assert.Error(t, err)
	assert.Equal(t, connect.CodeOf(err), connect.CodeDeadlineExceeded)
	t.Successf("successful timeout on sleep")
}

var testMetadata = metadata.MD{
	"key1": []string{"value1"},
	"key2": []string{"value2"},
}

// DoCancelAfterBegin cancels the RPC after metadata has been sent but before payloads are sent.
func DoCancelAfterBegin(t crosstesting.TB, client connectpb.TestServiceClient) {
	// TODO(doria): don't use grpc metadata library here...?
	ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(context.Background(), testMetadata))
	stream := client.StreamingInputCall(ctx)
	assert.NotNil(t, stream)
	cancel()
	_, err := stream.CloseAndReceive()
	assert.Equal(t, connect.CodeOf(err), connect.CodeCanceled)
	t.Successf("successful cancel after begin")
}

// DoCancelAfterFirstResponse cancels the RPC after receiving the first message from the server.
func DoCancelAfterFirstResponse(t crosstesting.TB, client connectpb.TestServiceClient) {
	ctx, cancel := context.WithCancel(context.Background())
	stream := client.FullDuplexCall(ctx)
	assert.NotNil(t, stream)
	respParam := []*testpb.ResponseParameters{
		{
			Size: 31415,
		},
	}
	pl, err := ClientNewPayload(t, testpb.PayloadType_COMPRESSABLE, 27182)
	require.NoError(t, err)
	req := &testpb.StreamingOutputCallRequest{
		ResponseType:       testpb.PayloadType_COMPRESSABLE,
		ResponseParameters: respParam,
		Payload:            pl,
	}
	require.NoError(t, stream.Send(req))
	_, err = stream.Receive()
	require.NoError(t, err)
	cancel()
	_, err = stream.Receive()
	assert.Equal(t, connect.CodeOf(err), connect.CodeCanceled)
	t.Successf("successful cancel after first response")
}

const (
	initialMetadataValue  = "test_initial_metadata_value"
	trailingMetadataValue = "\x0a\x0b\x0a\x0b\x0a\x0b"
)

func validateMetadata(
	t crosstesting.TB,
	header http.Header,
	trailer http.Header,
	expectedStringHeaders map[string][]string,
	expectedBinaryHeaders map[string][][]byte,
) {
	for key, values := range expectedStringHeaders {
		assert.Equal(t, len(values), len(header.Values(key)))
		valuesMap := map[string]struct{}{}
		for _, value := range values {
			valuesMap[value] = struct{}{}
		}
		for _, headerValue := range header.Values(key) {
			_, ok := valuesMap[headerValue]
			assert.True(t, ok)
		}
	}
	for key, values := range expectedBinaryHeaders {
		assert.Equal(t, len(values), len(trailer.Values(key)))
		valuesMap := map[string]struct{}{}
		for _, value := range values {
			valuesMap[string(value)] = struct{}{}
		}
		for _, trailerValue := range trailer.Values(key) {
			decodedTrailerValue, err := connect.DecodeBinaryHeader(trailerValue)
			assert.NoError(t, err)
			_, ok := valuesMap[string(decodedTrailerValue)]
			assert.True(t, ok)
		}
	}
}

// DoCustomMetadata checks that metadata is echoed back to the client.
func DoCustomMetadata(t crosstesting.TB, client connectpb.TestServiceClient) {
	customMetadataTest(
		t,
		client,
		map[string][]string{
			initialMetadataKey: {initialMetadataValue},
		},
		map[string][][]byte{
			trailingMetadataKey: {[]byte(trailingMetadataValue)},
		},
	)
	t.Successf("successful custom metadata")
}

// DoDuplicateCustomMetadata adds duplicated metadata keys and checks that the metadata is echoed back
// to the client.
func DoDuplicatedCustomMetadata(t crosstesting.TB, client connectpb.TestServiceClient) {
	customMetadataTest(
		t,
		client,
		map[string][]string{
			initialMetadataKey: {initialMetadataValue, initialMetadataValue + ",more_stuff"},
		},
		map[string][][]byte{
			trailingMetadataKey: {[]byte(trailingMetadataValue), []byte(trailingMetadataValue + "\x0a")},
		},
	)
	t.Successf("successful duplicated custom metadata")
}

func customMetadataTest(
	t crosstesting.TB,
	client connectpb.TestServiceClient,
	customMetadataString map[string][]string,
	customMetadataBinary map[string][][]byte,
) {
	// Testing with UnaryCall.
	payload, err := ClientNewPayload(t, testpb.PayloadType_COMPRESSABLE, 1)
	require.NoError(t, err)
	req := &testpb.SimpleRequest{
		ResponseType: testpb.PayloadType_COMPRESSABLE,
		ResponseSize: int32(1),
		Payload:      payload,
	}
	ctx := context.Background()
	connectReq := connect.NewRequest(req)
	for key, values := range customMetadataString {
		for _, value := range values {
			connectReq.Header().Add(key, value)
		}
	}
	for key, values := range customMetadataBinary {
		for _, value := range values {
			connectReq.Header().Add(key, connect.EncodeBinaryHeader(value))
		}
	}
	reply, err := client.UnaryCall(
		ctx,
		connectReq,
	)
	require.NoError(t, err)
	assert.Equal(t, reply.Msg.GetPayload().GetType(), testpb.PayloadType_COMPRESSABLE)
	assert.Equal(t, len(reply.Msg.GetPayload().GetBody()), 1)
	validateMetadata(t, reply.Header(), reply.Trailer(), customMetadataString, customMetadataBinary)

	// Testing with FullDuplex.
	stream := client.FullDuplexCall(ctx)
	assert.NotNil(t, stream)
	respParam := []*testpb.ResponseParameters{
		{
			Size: 1,
		},
	}
	streamReq := &testpb.StreamingOutputCallRequest{
		ResponseType:       testpb.PayloadType_COMPRESSABLE,
		ResponseParameters: respParam,
		Payload:            payload,
	}
	for key, values := range customMetadataString {
		for _, value := range values {
			stream.RequestHeader().Add(key, value)
		}
	}
	for key, values := range customMetadataBinary {
		for _, value := range values {
			stream.RequestHeader().Add(key, connect.EncodeBinaryHeader(value))
		}
	}
	require.NoError(t, stream.Send(streamReq))
	_, err = stream.Receive()
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	_, err = stream.Receive()
	assert.True(t, errors.Is(err, io.EOF))
	validateMetadata(t, stream.ResponseHeader(), stream.ResponseTrailer(), customMetadataString, customMetadataBinary)
}

// DoStatusCodeAndMessage checks that the status code is propagated back to the client.
func DoStatusCodeAndMessage(t crosstesting.TB, client connectpb.TestServiceClient) {
	code := int32(connect.CodeUnknown)
	msg := "test status message"
	expectedErr := connect.NewError(
		connect.CodeUnknown,
		errors.New(msg),
	)
	respStatus := &testpb.EchoStatus{
		Code:    code,
		Message: msg,
	}
	// Test UnaryCall.
	req := &testpb.SimpleRequest{
		ResponseStatus: respStatus,
	}
	_, err := client.UnaryCall(context.Background(), connect.NewRequest(req))
	assert.Error(t, err)
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnknown)
	assert.Equal(t, err.Error(), expectedErr.Error())
	// Test FullDuplexCall.
	stream := client.FullDuplexCall(context.Background())
	assert.NotNil(t, stream)
	streamReq := &testpb.StreamingOutputCallRequest{
		ResponseStatus: respStatus,
	}
	require.NoError(t, stream.Send(streamReq))
	require.NoError(t, stream.CloseSend())
	_, err = stream.Receive()
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnknown)
	assert.Equal(t, err.Error(), expectedErr.Error())
	t.Successf("successful code and message")
}

// DoSpecialStatusMessage verifies Unicode and whitespace is correctly processed
// in status message.
func DoSpecialStatusMessage(t crosstesting.TB, client connectpb.TestServiceClient) {
	code := int32(connect.CodeUnknown)
	msg := "\t\ntest with whitespace\r\nand Unicode BMP ☺ and non-BMP 😈\t\n"
	expectedErr := connect.NewError(connect.CodeUnknown, errors.New(msg))
	req := &testpb.SimpleRequest{
		ResponseStatus: &testpb.EchoStatus{
			Code:    code,
			Message: msg,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := client.UnaryCall(ctx, connect.NewRequest(req))
	assert.Error(t, err)
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnknown)
	assert.Equal(t, err.Error(), expectedErr.Error())
	t.Successf("successful code and message")
}

// DoUnimplementedService attempts to call a method from an unimplemented service.
func DoUnimplementedService(t crosstesting.TB, client connectpb.UnimplementedServiceClient) {
	_, err := client.UnimplementedCall(context.Background(), connect.NewRequest(&testpb.Empty{}))
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnimplemented)
	t.Successf("successful unimplemented service")
}

func DoFailWithNonASCIIError(t crosstesting.TB, client connectpb.TestServiceClient, args ...grpc.CallOption) {
	reply, err := client.FailUnaryCall(
		context.Background(),
		connect.NewRequest(
			&testpb.SimpleRequest{
				ResponseType: testpb.PayloadType_COMPRESSABLE,
			},
		),
	)
	assert.Nil(t, reply)
	assert.Error(t, err)
	assert.Equal(t, connect.CodeOf(err), connect.CodeResourceExhausted)
	assert.Equal(t, err.Error(), connect.CodeResourceExhausted.String()+": "+NonASCIIErrMsg)
	t.Successf("successful fail call with non-ASCII error")
}

// DoUnresolvableHost attempts to call a method to an unresolvable host.
func DoUnresolvableHost(t crosstesting.TB, client connectpb.TestServiceClient, args ...grpc.CallOption) {
	reply, err := client.EmptyCall(
		context.Background(),
		connect.NewRequest(&testpb.Empty{}),
	)
	assert.Nil(t, reply)
	assert.Error(t, err)
	assert.Equal(t, connect.CodeOf(err), connect.CodeUnavailable)
	t.Successf("successful fail call with unresolvable call")
}

func doOneSoakIteration(ctx context.Context, t crosstesting.TB, tc connectpb.TestServiceClient, resetChannel bool, serverAddr string) (latency time.Duration, err error) {
	start := time.Now()
	client := tc
	if resetChannel {
		newClient := connectpb.NewTestServiceClient(&http.Client{}, serverAddr)
		client = newClient
	}
	// per test spec, don't include channel shutdown in latency measurement
	defer func() { latency = time.Since(start) }()
	// do a large-unary RPC
	pl, err := ClientNewPayload(t, testpb.PayloadType_COMPRESSABLE, largeReqSize)
	require.NoError(t, err)
	req := &testpb.SimpleRequest{
		ResponseType: testpb.PayloadType_COMPRESSABLE,
		ResponseSize: int32(largeRespSize),
		Payload:      pl,
	}
	reply, err := client.UnaryCall(ctx, connect.NewRequest(req))
	require.NoError(t, err)
	assert.Equal(t, reply.Msg.GetPayload().GetType(), testpb.PayloadType_COMPRESSABLE)
	assert.Equal(t, len(reply.Msg.GetPayload().GetBody()), largeRespSize)
	return
}

// DoSoakTest runs large unary RPCs in a loop for a configurable number of times, with configurable failure thresholds.
// If resetChannel is false, then each RPC will be performed on client. Otherwise, each RPC will be performed on a new
// stub that is created with the provided server address and dial options.
func DoSoakTest(t crosstesting.TB, client connectpb.TestServiceClient, serverAddr string, resetChannel bool, soakIterations int, maxFailures int, perIterationMaxAcceptableLatency time.Duration, overallDeadline time.Time) {
	ctx, cancel := context.WithDeadline(context.Background(), overallDeadline)
	defer cancel()
	iterationsDone := 0
	totalFailures := 0
	hopts := stats.HistogramOptions{
		NumBuckets:     20,
		GrowthFactor:   1,
		BaseBucketSize: 1,
		MinValue:       0,
	}
	histogram := stats.NewHistogram(hopts)
	for i := 0; i < soakIterations; i++ {
		if time.Now().After(overallDeadline) {
			break
		}
		iterationsDone++
		latency, err := doOneSoakIteration(ctx, t, client, resetChannel, serverAddr)
		latencyMs := int64(latency / time.Millisecond)
		_ = histogram.Add(latencyMs)
		if err != nil {
			totalFailures++
			_, _ = fmt.Fprintf(os.Stderr, "soak iteration: %d elapsed_ms: %d failed: %s\n", i, latencyMs, err)
			continue
		}
		if latency > perIterationMaxAcceptableLatency {
			totalFailures++
			_, _ = fmt.Fprintf(os.Stderr, "soak iteration: %d elapsed_ms: %d exceeds max acceptable latency: %d\n", i, latencyMs, perIterationMaxAcceptableLatency.Milliseconds())
			continue
		}
		_, _ = fmt.Fprintf(os.Stderr, "soak iteration: %d elapsed_ms: %d succeeded\n", i, latencyMs)
	}
	var b bytes.Buffer
	histogram.Print(&b)
	_, _ = fmt.Fprintln(os.Stderr, "Histogram of per-iteration latencies in milliseconds:")
	_, _ = fmt.Fprintln(os.Stderr, b.String())
	_, _ = fmt.Fprintf(os.Stderr, "soak test ran: %d / %d iterations. total failures: %d. max failures threshold: %d. See breakdown above for which iterations succeeded, failed, and why for more info.\n", iterationsDone, soakIterations, totalFailures, maxFailures)
	assert.True(t, iterationsDone >= soakIterations)
	assert.True(t, totalFailures <= maxFailures)
}
