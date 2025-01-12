/*
Copyright 2020 Intel Corporation.

SPDX-License-Identifier: Apache-2.0
*/

package deploy

import (
	"context"
	"fmt"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	e2essh "k8s.io/kubernetes/test/e2e/framework/ssh"

	"github.com/intel/pmem-csi/pkg/k8sutil"
	"github.com/intel/pmem-csi/pkg/version"
	. "github.com/onsi/gomega"
)

type Cluster struct {
	nodeIPs []string
	cs      kubernetes.Interface
	dc      dynamic.Interface
	cfg     *rest.Config

	version     *version.Version
	isOpenShift bool
}

func NewCluster(cs kubernetes.Interface, dc dynamic.Interface, cfg *rest.Config) (*Cluster, error) {
	cluster := &Cluster{
		cs:  cs,
		dc:  dc,
		cfg: cfg,
	}

	hosts, err := e2essh.NodeSSHHosts(cs)
	if err != nil {
		return nil, fmt.Errorf("find external/internal IPs for every node: %v", err)
	}
	if len(hosts) <= 1 {
		return nil, fmt.Errorf("expected one master and one worker node, only got: %v", hosts)
	}
	for _, sshHost := range hosts {
		host := strings.Split(sshHost, ":")[0] // Instead of duplicating the NodeSSHHosts logic we simply strip the ssh port.
		cluster.nodeIPs = append(cluster.nodeIPs, host)
	}
	version, err := k8sutil.GetKubernetesVersion(cfg)
	if err != nil {
		return nil, err
	}
	cluster.version = version
	isOpenShift, err := k8sutil.IsOpenShift(cfg)
	if err != nil {
		return nil, err
	}
	cluster.isOpenShift = isOpenShift
	return cluster, nil
}

func (c *Cluster) ClientSet() kubernetes.Interface {
	return c.cs
}

func (c *Cluster) Config() *rest.Config {
	return c.cfg
}

// NumNodes returns the total number of nodes in the cluster.
// Node #0 is the master node, the rest are workers.
func (c *Cluster) NumNodes() int {
	return len(c.nodeIPs)
}

// NodeIP returns the IP address of a certain node.
func (c *Cluster) NodeIP(node int) string {
	return c.nodeIPs[node]
}

// NodeServiceAddress returns the gRPC dial address for a certain port on a certain nodes.
func (c *Cluster) NodeServiceAddress(node int, port int) string {
	return fmt.Sprintf("dns:///%s:%d", c.nodeIPs[node], port)
}

// GetServicePort looks up the node port of a service.
func (c *Cluster) GetServicePort(ctx context.Context, serviceName, namespace string) (int, error) {
	service, err := c.cs.CoreV1().Services(namespace).Get(ctx, serviceName, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	return int(service.Spec.Ports[0].NodePort), nil
}

// WaitForServicePort waits for the service to appear and returns its ports.
func (c *Cluster) WaitForServicePort(serviceName, namespace string) int {
	var port int
	Eventually(func() bool {
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		port, err = c.GetServicePort(ctx, serviceName, namespace)
		return err == nil && port != 0
	}, "3m").Should(BeTrue(), "%s service running", serviceName)
	return port
}

// GetAppInstance looks for a pod with certain labels and a specific host or pod IP.
// The IP may also be empty.
func (c *Cluster) GetAppInstance(ctx context.Context, appLabels labels.Set, ip, namespace string) (*v1.Pod, error) {
	pods, err := c.cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: appLabels.String()})
	if err != nil {
		return nil, err
	}
	for _, p := range pods.Items {
		if ip == "" || p.Status.HostIP == ip || p.Status.PodIP == ip {
			return &p, nil
		}
	}
	return nil, fmt.Errorf("no app %s in namespace %q with IP %q found", appLabels, namespace, ip)
}

// WaitForAppInstance waits for a running pod which matches the app
// label, optional host or pod IP, and namespace.
func (c *Cluster) WaitForAppInstance(appLabels labels.Set, ip, namespace string) *v1.Pod {
	var pod *v1.Pod
	Eventually(func() bool {
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		pod, err = c.GetAppInstance(ctx, appLabels, ip, namespace)
		return err == nil && pod.Status.Phase == v1.PodRunning
	}, "3m").Should(BeTrue(), "%s app running on host %s in '%s' namespace", appLabels, ip, namespace)
	return pod
}

func (c *Cluster) GetDaemonSet(ctx context.Context, setName, namespace string) (*appsv1.DaemonSet, error) {
	return c.cs.AppsV1().DaemonSets(namespace).Get(ctx, setName, metav1.GetOptions{})
}

func (c *Cluster) WaitForDaemonSet(setName, namespace string) *appsv1.DaemonSet {
	var set *appsv1.DaemonSet
	Eventually(func() bool {
		var err error
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		set, err = c.GetDaemonSet(ctx, setName, namespace)
		return err == nil
	}, "3m").Should(BeTrue(), "%s DaemonSet running", setName)
	return set
}

func (c *Cluster) GetStatefulSet(ctx context.Context, setName, namespace string) (*appsv1.StatefulSet, error) {
	return c.cs.AppsV1().StatefulSets(namespace).Get(ctx, setName, metav1.GetOptions{})
}

// StorageCapacitySupported checks that the v1beta1 CSIStorageCapacity API is supported.
// It only checks the Kubernetes version.
func (c *Cluster) StorageCapacitySupported() bool {
	return c.version.Compare(1, 21) >= 0
}
