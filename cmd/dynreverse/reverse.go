// Copyright 2025 Tobias Hintze
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

package main

import (
	"context"

	"github.com/paraopsde/go-x/pkg/util"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/thz/dynreverse/pkg/reverse"
)

type ReverseArgs struct {
	UpstreamEndpoint string
	ListenAddress    string
	Verbose          bool
}

func reverseCmd() *cobra.Command {
	reverseArgs := &ReverseArgs{}

	cmd := &cobra.Command{
		Use:   "reverse",
		Short: "Start dynamic reverse proxy",
		Run: func(cmd *cobra.Command, args []string) {
			level := zap.InfoLevel
			if reverseArgs.Verbose {
				level = zap.DebugLevel
			}
			log := util.NewLoggerWithLevel(level)
			ctx := util.CtxWithLog(context.Background(), log)
			proxy := reverse.NewProxy(reverse.ProxyOpts{
				UpstreamEndpoint: reverseArgs.UpstreamEndpoint,
				ListenAddress:    reverseArgs.ListenAddress,
			})
			if err := proxy.Run(ctx); err != nil {
				log.Error("proxy run failed", zap.Error(err))
			}
		},
	}

	cmd.Flags().StringVar(&reverseArgs.UpstreamEndpoint, "upstream-endpoint", "", "Upstream endpoint")
	cmd.Flags().StringVar(&reverseArgs.ListenAddress, "listen-address", ":50001", "Listen address")
	cmd.Flags().BoolVarP(&reverseArgs.Verbose, "verbose", "v", false, "Verbose output")

	return cmd
}
