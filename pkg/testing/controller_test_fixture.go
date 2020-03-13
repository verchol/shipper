package testing

import (
	"fmt"

	shipper "github.com/bookingcom/shipper/pkg/apis/shipper/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
)

type ControllerTestFixture struct {
	*FakeCluster

	Clusters           map[string]*FakeCluster
	ClusterClientStore *FakeClusterClientStore

	Recorder *record.FakeRecorder
}

func NewControllerTestFixture() *ControllerTestFixture {
	const recorderBufSize = 42

	store := NewFakeClusterClientStore(map[string]*FakeCluster{})
	fakeCluster := NewNamedFakeCluster("mgmt")

	return &ControllerTestFixture{
		FakeCluster: fakeCluster,

		Clusters:           make(map[string]*FakeCluster),
		ClusterClientStore: store,

		Recorder: record.NewFakeRecorder(recorderBufSize),
	}
}

func (f *ControllerTestFixture) AddNamedCluster(name string) *FakeCluster {
	cluster := NewNamedFakeCluster(name)

	f.Clusters[cluster.Name] = cluster
	f.ClusterClientStore.AddCluster(cluster)

	return cluster
}

func (f *ControllerTestFixture) AddCluster() *FakeCluster {
	name := fmt.Sprintf("cluster-%d", len(f.Clusters))
	return f.AddNamedCluster(name)
}

func (f *ControllerTestFixture) DynamicClientBuilder(
	kind *schema.GroupVersionKind,
	restConfig *rest.Config,
	cluster *shipper.Cluster,
) (dynamic.Interface, error) {
	if fdc, ok := f.Clusters[cluster.Name]; ok {
		return fdc.DynamicClient, nil
	}
	return nil, fmt.Errorf("couldn't find client for %q", cluster.Name)
}

func (f *ControllerTestFixture) Run(stopCh chan struct{}) {
	f.ShipperInformerFactory.Start(stopCh)
	f.ShipperInformerFactory.WaitForCacheSync(stopCh)
	f.ClusterClientStore.Run(stopCh)
}
