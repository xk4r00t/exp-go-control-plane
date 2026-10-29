// Copyright 2020 Envoyproxy Authors
//
//   Licensed under the Apache License, Version 2.0 (the "License");
//   you may not use this file except in compliance with the License.
//   You may obtain a copy of the License at
//
//       http://www.apache.org/licenses/LICENSE-2.0
//
//   Unless required by applicable law or agreed to in writing, software
//   distributed under the License is distributed on an "AS IS" BASIS,
//   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//   See the License for the specific language governing permissions and
//   limitations under the License.

package main

import (
	"context"
	"flag"
	"os"
	"strconv"
	"time"

	"github.com/envoyproxy/go-control-plane/internal/example"
	"github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	"github.com/envoyproxy/go-control-plane/pkg/server/v3"
	"github.com/envoyproxy/go-control-plane/pkg/test/v3"
)

var (
	l      example.Logger
	port   uint
	nodeID string
)

func init() {
	l = example.Logger{}

	flag.BoolVar(&l.Debug, "debug", false, "Enable xDS server debug logging")

	// The port that this xDS server listens on
	flag.UintVar(&port, "port", 18000, "xDS management server port")

	// Tell Envoy to use this Node ID
	flag.StringVar(&nodeID, "nodeID", "test-id", "Node ID")
}

// push builds snapshot `version` (rewriting example.ConfigFile) and puts it in the cache.
// The cache then sends it to every Envoy connected with nodeID.
func push(c cache.SnapshotCache, version int) error {
	snapshot, err := example.GenerateSnapshot(strconv.Itoa(version))
	if err != nil {
		return err
	}
	if err := snapshot.Consistent(); err != nil {
		return err
	}
	l.Infof("push v%d from %s", version, example.SourceFile)
	return c.SetSnapshot(context.Background(), nodeID, snapshot)
}

func main() {
	flag.Parse()

	// Create a cache
	cache := cache.NewSnapshotCache(false, cache.IDHash{}, l)

	// Push the first snapshot
	version := 1
	if err := push(cache, version); err != nil {
		l.Errorf("snapshot error: %v", err)
		os.Exit(1)
	}

	// Push again when the file changes; a new version makes Envoy fetch it
	go func() {
		last := time.Now()
		for range time.Tick(2 * time.Second) {
			st, err := os.Stat(example.SourceFile)
			if err != nil || !st.ModTime().After(last) {
				continue
			}
			last = st.ModTime()
			if err := push(cache, version+1); err != nil {
				l.Errorf("not pushed, Envoy keeps v%d: %v", version, err)
				continue
			}
			version++
		}
	}()

	// Run the xDS server
	ctx := context.Background()
	cb := &test.Callbacks{Debug: l.Debug}
	srv := server.NewServer(ctx, cache, cb)
	example.RunServer(srv, port)
}
