// This is the test server implementation from the grpc-go interop test_utils.go file,
// https://github.com/grpc/grpc-go/blob/master/interop/test_utils.go

/*
 *
 * Copyright 2014 gRPC authors.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package interopgrpc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	testpb "github.com/bufbuild/connect-crosstest/internal/gen/proto/go/grpc/testing"
	"github.com/bufbuild/connect-crosstest/internal/interopconnect"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// NewTestServer creates a test server for test service.
func NewTestServer() testpb.TestServiceServer {
	return &testServer{}
}

type testServer struct {
	testpb.UnimplementedTestServiceServer
}

func (s *testServer) EmptyCall(ctx context.Context, in *testpb.Empty) (*testpb.Empty, error) {
	return new(testpb.Empty), nil
}

func serverNewPayload(payloadType testpb.PayloadType, size int32) (*testpb.Payload, error) {
	if size < 0 {
		return nil, fmt.Errorf("requested a response with invalid length %d", size)
	}
	body := make([]byte, size)
	switch payloadType {
	case testpb.PayloadType_COMPRESSABLE:
	default:
		return nil, fmt.Errorf("unsupported payload type: %d", payloadType)
	}
	return &testpb.Payload{
		Type: payloadType,
		Body: body,
	}, nil
}

func createMetadataPairs(metadataKey string, metadata []string) []string {
	metadataPairs := make([]string, len(metadata)*2)
	for i, metadataValue := range metadata {
		metadataPairs[i*2] = metadataKey
		metadataPairs[i*2+1] = metadataValue
	}
	return metadataPairs
}

func (s *testServer) UnaryCall(ctx context.Context, req *testpb.SimpleRequest) (*testpb.SimpleResponse, error) {
	responseStatus := req.GetResponseStatus()
	var header, trailer metadata.MD
	if data, ok := metadata.FromIncomingContext(ctx); ok {
		if initialMetadata, ok := data[initialMetadataKey]; ok {
			metadataPairs := createMetadataPairs(initialMetadataKey, initialMetadata)
			header = metadata.Pairs(metadataPairs...)
		}
		if trailingMetadata, ok := data[trailingMetadataKey]; ok {
			trailingMetadataPairs := createMetadataPairs(trailingMetadataKey, trailingMetadata)
			trailer = metadata.Pairs(trailingMetadataPairs...)
		}
	}
	if header != nil {
		if err := grpc.SendHeader(ctx, header); err != nil {
			return nil, err
		}
	}
	if trailer != nil {
		if err := grpc.SetTrailer(ctx, trailer); err != nil {
		}
	}
	if responseStatus != nil && responseStatus.Code != 0 {
		return nil, status.Error(codes.Code(responseStatus.Code), responseStatus.Message)
	}
	pl, err := serverNewPayload(req.GetResponseType(), req.GetResponseSize())
	if err != nil {
		return nil, err
	}
	return &testpb.SimpleResponse{
		Payload: pl,
	}, nil
}

// FailUnaryCall is an additional RPC added for cross tests.
func (s *testServer) FailUnaryCall(ctx context.Context, in *testpb.SimpleRequest) (*testpb.SimpleResponse, error) {
	return nil, status.Error(codes.ResourceExhausted, interopconnect.NonASCIIErrMsg)
}

func (s *testServer) StreamingOutputCall(args *testpb.StreamingOutputCallRequest, stream testpb.TestService_StreamingOutputCallServer) error {
	cs := args.GetResponseParameters()
	for _, c := range cs {
		if us := c.GetIntervalUs(); us > 0 {
			time.Sleep(time.Duration(us) * time.Microsecond)
		}
		pl, err := serverNewPayload(args.GetResponseType(), c.GetSize())
		if err != nil {
			return err
		}
		if err := stream.Send(&testpb.StreamingOutputCallResponse{
			Payload: pl,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *testServer) StreamingInputCall(stream testpb.TestService_StreamingInputCallServer) error {
	var sum int
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.SendAndClose(&testpb.StreamingInputCallResponse{
				AggregatedPayloadSize: int32(sum),
			})
		}
		if err != nil {
			return err
		}
		p := req.GetPayload().GetBody()
		sum += len(p)
	}
}

func (s *testServer) FullDuplexCall(stream testpb.TestService_FullDuplexCallServer) error {
	if data, ok := metadata.FromIncomingContext(stream.Context()); ok {
		if initialMetadata, ok := data[initialMetadataKey]; ok {
			var metadataPairs []string
			for _, metadataValue := range initialMetadata {
				metadataPairs = append(metadataPairs, initialMetadataKey)
				metadataPairs = append(metadataPairs, metadataValue)
			}
			header := metadata.Pairs(metadataPairs...)
			if err := stream.SendHeader(header); err != nil {
				return err
			}
		}
		if trailingMetadata, ok := data[trailingMetadataKey]; ok {
			var trailingMetadataPairs []string
			for _, trailingMetadataValue := range trailingMetadata {
				trailingMetadataPairs = append(trailingMetadataPairs, trailingMetadataKey)
				trailingMetadataPairs = append(trailingMetadataPairs, trailingMetadataValue)
			}
			trailer := metadata.Pairs(trailingMetadataPairs...)
			stream.SetTrailer(trailer)
		}
	}
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			// read done.
			return nil
		}
		if err != nil {
			return err
		}
		st := req.GetResponseStatus()
		if st != nil && st.Code != 0 {
			return status.Error(codes.Code(st.Code), st.Message)
		}
		cs := req.GetResponseParameters()
		for _, c := range cs {
			if us := c.GetIntervalUs(); us > 0 {
				time.Sleep(time.Duration(us) * time.Microsecond)
			}
			pl, err := serverNewPayload(req.GetResponseType(), c.GetSize())
			if err != nil {
				return err
			}
			if err := stream.Send(&testpb.StreamingOutputCallResponse{
				Payload: pl,
			}); err != nil {
				return err
			}
		}
	}
}

func (s *testServer) HalfDuplexCall(stream testpb.TestService_HalfDuplexCallServer) error {
	var msgBuf []*testpb.StreamingOutputCallRequest
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			// read done.
			break
		}
		if err != nil {
			return err
		}
		msgBuf = append(msgBuf, req)
	}
	for _, msg := range msgBuf {
		cs := msg.GetResponseParameters()
		for _, c := range cs {
			if us := c.GetIntervalUs(); us > 0 {
				time.Sleep(time.Duration(us) * time.Microsecond)
			}
			pl, err := serverNewPayload(msg.GetResponseType(), c.GetSize())
			if err != nil {
				return err
			}
			if err := stream.Send(&testpb.StreamingOutputCallResponse{
				Payload: pl,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}
