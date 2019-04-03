/*
Copyright 2019 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"fmt"
	"sync"
	"time"

	"github.com/knative/pkg/client/clientset/versioned/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var (
	// Client is a global controller-runtime Client for use by reconcilers.
	Client     client.Client
	clientOnce sync.Once
)

// StartFunc is a function used to start the Client.
type StartFunc func(<-chan struct{}) error

// SetClient creates the package-level Client. The returned StartFunc must be called
// before the client can be used.
func SetClient(config *rest.Config) (StartFunc, error) {
	var err error
	var start StartFunc
	clientOnce.Do(func() {
		Client, start, err = newClient(config)
		if err != nil {
			return
		}
	})
	return start, err
}

// UnsafeSetClient sets the package-level Client manually for testing. This
// function is not thread-safe.
func UnsafeSetClient(c client.Client) {
	Client = c
}

func newClient(config *rest.Config) (client.Client, StartFunc, error) {
	if config == nil {
		return nil, nil, fmt.Errorf("must specify Config")
	}

	mapper, err := apiutil.NewDiscoveryRESTMapper(config)
	if err != nil {
		return nil, nil, err
	}

	//TODO scheme option
	clientScheme := scheme.Scheme
	//TODO resync option
	resyncPeriod := 10 * time.Hour
	//TODO namespace option

	cache, err := cache.New(config, cache.Options{Scheme: clientScheme, Mapper: mapper, Resync: &resyncPeriod})
	if err != nil {
		return nil, nil, err
	}

	apiClient, err := client.New(config, client.Options{Scheme: clientScheme, Mapper: mapper})
	if err != nil {
		return nil, nil, err
	}

	cl := &client.DelegatingClient{
		Reader: &client.DelegatingReader{
			CacheReader:  cache,
			ClientReader: apiClient,
		},
		Writer:       apiClient,
		StatusClient: apiClient,
	}

	return cl, cache.Start, nil
}
