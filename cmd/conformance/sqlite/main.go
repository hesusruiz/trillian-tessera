// Copyright 2024 The Tessera authors. All Rights Reserved.
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

// sqlite is a simple personality allowing to run conformance/compliance/performance tests and showing how to use the Tessera SQLite storage implmentation.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"time"

	tessera "github.com/transparency-dev/trillian-tessera"
	"github.com/transparency-dev/trillian-tessera/api/layout"
	"github.com/transparency-dev/trillian-tessera/storage/sqlite"
	"golang.org/x/mod/sumdb/note"
	"k8s.io/klog/v2"
)

var (
	dbConnMaxLifetime         = flag.Duration("db_conn_max_lifetime", 3*time.Minute, "")
	listen                    = flag.String("listen", ":2024", "Address:port to listen on")
	privateKeyPath            = flag.String("private_key_path", "", "Location of private key file")
	publishInterval           = flag.Duration("publish_interval", 3*time.Second, "How frequently to publish updated checkpoints")
	additionalPrivateKeyPaths = []string{}
)

func init() {
	flag.Func("additional_private_key_path", "Location of additional private key file, may be specified multiple times", func(s string) error {
		additionalPrivateKeyPaths = append(additionalPrivateKeyPaths, s)
		return nil
	})
}

func main() {
	klog.InitFlags(nil)
	flag.Parse()
	if *privateKeyPath == "" {

	}
	ctx := context.Background()

	db := createDatabaseOrDie()
	noteSigner, additionalSigners := createSignersOrDie()

	// Initialise the Tessera SQLite storage
	storage, err := sqlite.New(ctx, db,
		tessera.WithCheckpointSigner(noteSigner, additionalSigners...),
		tessera.WithCheckpointInterval(*publishInterval),
	)
	if err != nil {
		klog.Exitf("Failed to create new SQLite storage: %v", err)
	}
	dedupeAdd := tessera.InMemoryDedupe(storage.Add, 256)

	// Set up the handlers for the tlog-tiles GET methods, and a custom handler for HTTP POSTs to /add
	configureTilesReadAPI(http.DefaultServeMux, storage)
	http.HandleFunc("POST /add", func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		idx, err := dedupeAdd(r.Context(), tessera.NewEntry(b))()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		if _, err = w.Write([]byte(fmt.Sprintf("%d", idx))); err != nil {
			klog.Errorf("/add: %v", err)
			return
		}
	})

	// TODO(mhutchinson): Change the listen flag to just a port, or fix up this address formatting
	klog.Infof("Environment variables useful for accessing this log:\n"+
		"export WRITE_URL=http://localhost%s/ \n"+
		"export READ_URL=http://localhost%s/ \n", *listen, *listen)
	// Serve HTTP requests until the process is terminated
	if err := http.ListenAndServe(*listen, http.DefaultServeMux); err != nil {
		klog.Exitf("ListenAndServe: %v", err)
	}
}

func createDatabaseOrDie() *sql.DB {

	db, err := sqlite.CreateTables()
	if err != nil {
		klog.Exitf("Failed to create tables: %v", err)
	}
	db.SetConnMaxLifetime(*dbConnMaxLifetime)

	return db
}

func createSignersOrDie() (note.Signer, []note.Signer) {
	s := createSignerOrDie(*privateKeyPath)
	a := []note.Signer{}
	for _, p := range additionalPrivateKeyPaths {
		a = append(a, createSignerOrDie(p))
	}
	return s, a
}

func createSignerOrDie(s string) note.Signer {
	var privateKey = "PRIVATE+KEY+example.com/log/testdata+33d7b496+AeymY/SZAX0jZcJ8enZ5FY1Dz+wTML2yWSkK+9DSF3eg"
	const publicKey = "example.com/log/testdata+33d7b496+AeHTu4Q3hEIMHNqc6fASMsq3rKNx280NI+oO5xCFkkSx"
	if len(s) > 0 {
		rawPrivateKey, err := os.ReadFile(s)
		if err != nil {
			klog.Exitf("Failed to read private key file %q: %v", s, err)
		}
		privateKey = string(rawPrivateKey)
	} else {
		fmt.Println("Public key:", publicKey)
	}
	noteSigner, err := note.NewSigner(privateKey)
	if err != nil {
		klog.Exitf("Failed to create new signer: %v", err)
	}
	return noteSigner
}

// configureTilesReadAPI adds the API methods from https://c2sp.org/tlog-tiles to the mux,
// routing the requests to the mysql storage.
// This method could be moved into the storage API as it's likely this will be
// the same for any implementation of a personality based on MySQL.
func configureTilesReadAPI(mux *http.ServeMux, storage *sqlite.Storage) {
	mux.HandleFunc("GET /checkpoint", func(w http.ResponseWriter, r *http.Request) {
		checkpoint, err := storage.ReadCheckpoint(r.Context())
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			klog.Errorf("/checkpoint: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		// Don't cache checkpoints as the endpoint refreshes regularly.
		// A personality that wanted to _could_ set a small cache time here which was no higher
		// than the checkpoint publish interval.
		w.Header().Set("Cache-Control", "no-cache")
		if _, err := w.Write(checkpoint); err != nil {
			klog.Errorf("/checkpoint: %v", err)
			return
		}
	})

	mux.HandleFunc("GET /tile/{level}/{index...}", func(w http.ResponseWriter, r *http.Request) {
		level, index, p, err := layout.ParseTileLevelIndexPartial(r.PathValue("level"), r.PathValue("index"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			if _, werr := w.Write([]byte(fmt.Sprintf("Malformed URL: %s", err.Error()))); werr != nil {
				klog.Errorf("/tile/{level}/{index...}: %v", werr)
			}
			return
		}
		tile, err := storage.ReadTile(r.Context(), level, index, p)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			klog.Errorf("/tile/{level}/{index...}: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

		if _, err := w.Write(tile); err != nil {
			klog.Errorf("/tile/{level}/{index...}: %v", err)
			return
		}
	})

	mux.HandleFunc("GET /tile/entries/{index...}", func(w http.ResponseWriter, r *http.Request) {
		index, p, err := layout.ParseTileIndexPartial(r.PathValue("index"))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			if _, werr := w.Write([]byte(fmt.Sprintf("Malformed URL: %s", err.Error()))); werr != nil {
				klog.Errorf("/tile/entries/{index...}: %v", werr)
			}
			return
		}

		entryBundle, err := storage.ReadEntryBundle(r.Context(), index, p)
		if err != nil {
			klog.Errorf("/tile/entries/{index...}: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if entryBundle == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")

		if _, err := w.Write(entryBundle); err != nil {
			klog.Errorf("/tile/entries/{index...}: %v", err)
			return
		}
	})
}
