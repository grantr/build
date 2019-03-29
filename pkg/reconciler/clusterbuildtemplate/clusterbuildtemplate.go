/*
Copyright 2018 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package clusterbuildtemplate

import (
	"context"

	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/cache"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/knative/pkg/controller"
	"github.com/knative/pkg/kmeta"
	"github.com/knative/pkg/logging"
	"github.com/knative/pkg/logging/logkey"

	"github.com/knative/build/pkg/apis/build/v1alpha1"
	buildscheme "github.com/knative/build/pkg/client/clientset/versioned/scheme"
	informers "github.com/knative/build/pkg/client/informers/externalversions/build/v1alpha1"
	"github.com/knative/build/pkg/reconciler"
	"github.com/knative/build/pkg/reconciler/buildtemplate"
	"github.com/knative/build/pkg/reconciler/clusterbuildtemplate/resources"
	caching "github.com/knative/caching/pkg/apis/caching/v1alpha1"
	cachinginformers "github.com/knative/caching/pkg/client/informers/externalversions/caching/v1alpha1"
	"github.com/knative/pkg/system"
)

const controllerAgentName = "clusterbuildtemplate-controller"

// Reconciler is the controller.Reconciler implementation for ClusterBuildTemplates resources
type Reconciler struct {
	// Sugared logger is easier to use but is not as performant as the
	// raw logger. In performance critical paths, call logger.Desugar()
	// and use the returned raw logger instead. In addition to the
	// performance benefits, raw logger also preserves type-safety at
	// the expense of slightly greater verbosity.
	Logger *zap.SugaredLogger
}

// Check that we implement the controller.Reconciler interface.
var _ controller.Reconciler = (*Reconciler)(nil)

func init() {
	// Add clusterbuildtemplate-controller types to the default Kubernetes Scheme so Events can be
	// logged for clusterbuildtemplate-controller types.
	buildscheme.AddToScheme(scheme.Scheme)
}

// NewController returns a new build template controller
func NewController(
	logger *zap.SugaredLogger,
	clusterBuildTemplateInformer informers.ClusterBuildTemplateInformer,
	imageInformer cachinginformers.ImageInformer,
) *controller.Impl {

	// Enrich the logs with controller name
	logger = logger.Named(controllerAgentName).With(zap.String(logkey.ControllerType, controllerAgentName))

	r := &Reconciler{
		Logger: logger,
	}
	impl := controller.NewImpl(r, logger, "ClusterBuildTemplates",
		reconciler.MustNewStatsReporter("ClusterBuildTemplates", r.Logger))

	logger.Info("Setting up event handlers")
	// Set up an event handler for when ClusterBuildTemplate resources change
	clusterBuildTemplateInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    impl.Enqueue,
		UpdateFunc: controller.PassNew(impl.Enqueue),
	})

	imageInformer.Informer().AddEventHandler(cache.FilteringResourceEventHandler{
		FilterFunc: controller.Filter(v1alpha1.SchemeGroupVersion.WithKind("ClusterBuildTemplate")),
		Handler: cache.ResourceEventHandlerFuncs{
			AddFunc:    impl.EnqueueControllerOf,
			UpdateFunc: controller.PassNew(impl.EnqueueControllerOf),
		},
	})

	return impl
}

// Reconcile implements controller.Reconciler
func (c *Reconciler) Reconcile(ctx context.Context, key string) error {
	logger := logging.FromContext(ctx)

	// Get the ClusterBuildTemplate resource with this key
	cbt := &v1alpha1.ClusterBuildTemplate{}
	err := controller.Client.Get(ctx, types.NamespacedName{Name: key}, cbt)
	if errors.IsNotFound(err) {
		// The ClusterBuildTemplate resource may no longer exist, in which case we stop processing.
		logger.Errorf("clusterbuildtemplate %q in work queue no longer exists", key)
		return nil
	} else if err != nil {
		return err
	}

	if err := c.reconcileImageCaches(ctx, cbt); err != nil {
		return err
	}

	return nil
}

func (c *Reconciler) reconcileImageCaches(ctx context.Context, cbt *v1alpha1.ClusterBuildTemplate) error {
	ics := resources.MakeImageCaches(cbt)

	eics := &caching.ImageList{}
	opts := &client.ListOptions{
		Namespace:     system.Namespace(),
		LabelSelector: kmeta.MakeVersionLabelSelector(cbt),
	}
	err := controller.Client.List(ctx, opts, eics)
	if err != nil {
		return err
	}

	// Make sure we have all of the desired caching resources.
	if err := buildtemplate.CreateMissingImageCaches(ctx, ics, eics.Items); err != nil {
		return err
	}

	// Delete any Image caches relevant to older versions of this resource.
	imagesToDelete := &caching.ImageList{}
	opts = &client.ListOptions{
		Namespace:     system.Namespace(),
		LabelSelector: kmeta.MakeOldVersionLabelSelector(cbt),
	}
	err = controller.Client.List(ctx, opts, imagesToDelete)
	if err != nil {
		return err
	}
	grp, _ := errgroup.WithContext(ctx)
	for _, d := range imagesToDelete.Items {
		d := d
		grp.Go(func() error {
			return controller.Client.Delete(ctx,
				&d,
				client.PropagationPolicy(metav1.DeletePropagationForeground),
			)
		})
	}

	// Wait for the deletes to complete.
	return grp.Wait()
}
