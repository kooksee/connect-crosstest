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

package main

import (
	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"net/url"

	"github.com/bufbuild/connect-crosstest/internal/console"
	connectpb "github.com/bufbuild/connect-crosstest/internal/gen/proto/connect/grpc/testing/testingconnect"
	testgrpc "github.com/bufbuild/connect-crosstest/internal/gen/proto/go/grpc/testing"
	interopconnect "github.com/bufbuild/connect-crosstest/internal/interop/connect"
	interopgrpc "github.com/bufbuild/connect-crosstest/internal/interop/grpc"
	"github.com/bufbuild/connect-go"
	"github.com/lucas-clemente/quic-go/http3"
	"github.com/spf13/cobra"
	"golang.org/x/net/http2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

type flags struct {
	host           string
	port           string
	implementation string
	certFile       string
	keyFile        string
}

func main() {
	flagset := flags{}
	rootCmd := &cobra.Command{
		Use:   "client",
		Short: "Starts a grpc or connect client, based on implementation",
		Run: func(cmd *cobra.Command, args []string) {
			run(flagset)
		},
	}
	rootCmd.Flags().StringVar(&flagset.host, "host", "127.0.0.1", "the host name of the test server")
	rootCmd.Flags().StringVar(&flagset.port, "port", "", "the port of the test server")
	rootCmd.Flags().StringVarP(
		&flagset.implementation,
		"implementation",
		"i",
		"connect",
		`the client implementation tested, accepted values are "connect-h2", "connect-h3" or "grpc-go"`,
	)
	rootCmd.Flags().StringVar(&flagset.certFile, "cert", "", "path to the TLS cert file")
	rootCmd.Flags().StringVar(&flagset.keyFile, "key", "", "path to the TLS key file")
	_ = rootCmd.MarkFlagRequired("port")
	_ = rootCmd.MarkFlagRequired("cert")
	_ = rootCmd.MarkFlagRequired("key")
	_ = rootCmd.Execute()
}

func run(flagset flags) {
	switch flagset.implementation {
	case "connect-h2", "connect-h3":
		serverURL, err := url.ParseRequestURI("https://" + net.JoinHostPort(flagset.host, flagset.port))
		if err != nil {
			log.Fatalf("invalid url: %s", "https://"+net.JoinHostPort(flagset.host, flagset.port))
		}
		client := connectpb.NewTestServiceClient(
			newClient(flagset.implementation, flagset.certFile, flagset.keyFile),
			serverURL.String(),
			connect.WithGRPC(),
		)
		interopconnect.DoEmptyUnaryCall(console.NewTB(), client)
		interopconnect.DoLargeUnaryCall(console.NewTB(), client)
		interopconnect.DoClientStreaming(console.NewTB(), client)
		interopconnect.DoServerStreaming(console.NewTB(), client)
		interopconnect.DoPingPong(console.NewTB(), client)
		interopconnect.DoEmptyStream(console.NewTB(), client)
		interopconnect.DoTimeoutOnSleepingServer(console.NewTB(), client)
		interopconnect.DoCancelAfterBegin(console.NewTB(), client)
		interopconnect.DoCancelAfterFirstResponse(console.NewTB(), client)
		interopconnect.DoCustomMetadata(console.NewTB(), client)
		interopconnect.DoStatusCodeAndMessage(console.NewTB(), client)
		interopconnect.DoSpecialStatusMessage(console.NewTB(), client)
		interopconnect.DoUnimplementedService(console.NewTB(), client)
		interopconnect.DoFailWithNonASCIIError(console.NewTB(), client)
	case "grpc-go":
		gconn, err := grpc.Dial(
			net.JoinHostPort(flagset.host, flagset.port),
			grpc.WithTransportCredentials(credentials.NewTLS(newTLSConfig(flagset.certFile, flagset.keyFile))),
		)
		if err != nil {
			log.Fatalf("failed grpc dial: %v", err)
		}
		defer gconn.Close()
		client := testgrpc.NewTestServiceClient(gconn)
		interopgrpc.DoEmptyUnaryCall(console.NewTB(), client)
		interopgrpc.DoLargeUnaryCall(console.NewTB(), client)
		interopgrpc.DoClientStreaming(console.NewTB(), client)
		interopgrpc.DoServerStreaming(console.NewTB(), client)
		interopgrpc.DoPingPong(console.NewTB(), client)
		interopgrpc.DoEmptyStream(console.NewTB(), client)
		interopgrpc.DoTimeoutOnSleepingServer(console.NewTB(), client)
		interopgrpc.DoCancelAfterBegin(console.NewTB(), client)
		interopgrpc.DoCancelAfterFirstResponse(console.NewTB(), client)
		interopgrpc.DoCustomMetadata(console.NewTB(), client)
		interopgrpc.DoStatusCodeAndMessage(console.NewTB(), client)
		interopgrpc.DoSpecialStatusMessage(console.NewTB(), client)
		interopgrpc.DoUnimplementedMethod(console.NewTB(), gconn)
		interopgrpc.DoUnimplementedService(console.NewTB(), client)
		interopgrpc.DoFailWithNonASCIIError(console.NewTB(), client)
	default:
		log.Fatalf(`must set --implementation or -i to "connect-h2", "connect-h3" or "grpc-go"`)
	}
}

func newClient(implementation, certFile, keyFile string) *http.Client {
	tlsConfig := newTLSConfig(certFile, keyFile)
	var transport http.RoundTripper
	switch implementation {
	case "connect-h2":
		transport = &http2.Transport{
			TLSClientConfig: tlsConfig,
		}
	case "connect-h3":
		transport = &http3.RoundTripper{
			TLSClientConfig: tlsConfig,
		}
	default:
		log.Fatalf("unknown implementation flag to create client")
	}
	// This is wildly insecure - don't do this in production!
	return &http.Client{
		Transport: transport,
	}
}

func newTLSConfig(certFile, keyFile string) *tls.Config {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatalf("Error creating x509 keypair from client cert file %s and client key file %s", certFile, keyFile)
	}
	caCert, err := ioutil.ReadFile("cert/CrosstestCA.crt")
	if err != nil {
		log.Fatalf("Error opening cert file")
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      caCertPool,
	}
}
