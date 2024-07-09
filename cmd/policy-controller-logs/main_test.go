package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"regexp"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func TestOpenShiftNamespace(t *testing.T) {
	clientset, err := clientset()
	if err != nil {
		t.Fatalf("failed to create clientset: %v", err)
	}

	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "scc-privileged",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups:     []string{"security.openshift.io"},
				Resources:     []string{"securitycontextconstraints"},
				ResourceNames: []string{"privileged"},
				Verbs:         []string{"use"},
			},
		},
	}
	_, err = clientset.RbacV1().ClusterRoles().Create(
		context.TODO(),
		clusterRole,
		metav1.CreateOptions{},
	)
	if err != nil {
		t.Fatalf("failed to create cluster role: %v", err)
	}

	for _, tt := range []struct {
		name      string
		namespace *corev1.Namespace
		options   metav1.CreateOptions
	}{
		{
			name: "should violate as openshift namespaces don't get synced",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "openshift-test-namespace",
				},
			},
			options: metav1.CreateOptions{},
		},
		{
			name: "should violate as syncer is disabled",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "syncer-off-namespace",
					Labels: map[string]string{
						"pod-security.kubernetes.io/warn":                "restricted",
						"pod-security.kubernetes.io/audit":               "restricted",
						"security.openshift.io/scc.podSecurityLabelSync": "false",
					},
				},
			},
			options: metav1.CreateOptions{
				FieldManager: "pod-security-admission-label-synchronization-controller",
			},
		},
		{
			name: "should not violate as syncer has at least one label",
			namespace: &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "syncer-with-one-label",
					Labels: map[string]string{
						"pod-security.kubernetes.io/warn": "restricted",
					},
				},
			},
			options: metav1.CreateOptions{
				FieldManager: "kube-edit",
			},
		},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err = clientset.CoreV1().Namespaces().Create(context.TODO(), tt.namespace, tt.options)
			if err != nil {
				t.Fatalf("failed to create namespace: %v", err)
			}

			sa := &corev1.ServiceAccount{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "privileged-sa",
					Namespace: tt.namespace.Name,
				},
			}

			_, err = clientset.CoreV1().ServiceAccounts(tt.namespace.Name).Create(
				context.TODO(), sa, metav1.CreateOptions{},
			)
			if err != nil {
				t.Fatalf("failed to create service account: %v", err)
			}

			roleBinding := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "privileged-sa-scc-privileged",
					Namespace: tt.namespace.Name,
				},
				Subjects: []rbacv1.Subject{
					{
						Kind:      "ServiceAccount",
						Name:      sa.Name,
						Namespace: tt.namespace.Name,
					},
				},
				RoleRef: rbacv1.RoleRef{
					Kind:     "ClusterRole",
					Name:     clusterRole.Name,
					APIGroup: "rbac.authorization.k8s.io",
				},
			}

			_, err := clientset.RbacV1().RoleBindings(tt.namespace.Name).Create(
				context.TODO(),
				roleBinding,
				metav1.CreateOptions{},
			)
			if err != nil {
				t.Fatalf("failed to create role binding: %v", err)
			}

			deployment := &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "privileged-deployment",
					Namespace: tt.namespace.Name,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: int32Ptr(1),
					Selector: &metav1.LabelSelector{
						MatchLabels: map[string]string{
							"app": "privileged-app",
						},
					},
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								"app": "privileged-app",
							},
						},
						Spec: corev1.PodSpec{
							ServiceAccountName: sa.Name,
							Containers: []corev1.Container{
								{
									Name:  "privileged-container",
									Image: "busybox",
									Command: []string{
										"sh",
										"-c",
										"echo 'Privileged container is running'; sleep infinity",
									},
									SecurityContext: &corev1.SecurityContext{
										Privileged: boolPtr(true),
									},
								},
							},
						},
					},
				},
			}

			_, err = clientset.AppsV1().Deployments(tt.namespace.Name).Create(
				context.TODO(),
				deployment,
				metav1.CreateOptions{},
			)
			if err != nil {
				t.Fatalf("failed to create deployment: %v", err)
			}

			t.Log("waiting for controller to sync namespace")
			time.Sleep(5 * time.Minute)

			pods, err := clientset.CoreV1().Pods("openshift-kube-apiserver-operator").List(context.TODO(), metav1.ListOptions{})
			if err != nil {
				t.Fatalf("failed to list pods: %v", err)
			}

			foundSomething := false
			for _, pod := range pods.Items {
				req := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
				podLogs, err := req.Stream(context.TODO())
				if err != nil {
					t.Fatalf("failed to get logs for pod %s/%s: %v", pod.Namespace, pod.Name, err)
				}
				defer podLogs.Close()

				buf := new(bytes.Buffer)
				_, err = io.Copy(buf, podLogs)
				if err != nil {
					fmt.Printf("Error reading logs for %s/%s: %v\n", pod.Namespace, pod.Name, err)
					return
				}

				logs := buf.String()
				re := regexp.MustCompile(fmt.Sprintf("= %s =", controllerName))
				matches := re.FindAllString(logs, -1)

				if len(matches) > 0 {
					filename := fmt.Sprintf("logs_%s_%s.txt", tt.name, time.Now().Format("20060102_150405"))
					err = os.WriteFile(filename, buf.Bytes(), 0644)
					if err != nil {
						t.Errorf("failed to write logs to file: %v", err)
					}

					foundSomething = true
				}
			}

			if !foundSomething {
				t.Errorf("expected to find logs for %s", tt.namespace.Name)
			}
		})
	}

}

func clientset() (*kubernetes.Clientset, error) {
	if kubeconfig := os.Getenv("KUBECONFIG"); kubeconfig != "" {
		config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			return nil, err
		}

		return kubernetes.NewForConfig(config)
	}

	return nil, fmt.Errorf("KUBECONFIG not set")
}
