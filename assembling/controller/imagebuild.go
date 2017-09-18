/*
Copyright 2016 - 2017 Huawei Technologies Co., Ltd. All rights reserved.

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

package controller

import (
	"archive/tar"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"time"

	log "github.com/Sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"github.com/Huawei/containerops/common"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"golang.org/x/net/context"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/clientcmd"
)

func GenerateAuthStr(username, password string) (string, error) {
	authConfig := types.AuthConfig{
		Username: username,
		Password: password,
	}
	encodedJSON, err := json.Marshal(authConfig)
	if err != nil {
		log.Error(err)
		return "", err
	}
	authStr := base64.URLEncoding.EncodeToString(encodedJSON)
	return authStr, nil

}

func InitK8SResourceInterfaces(kubeconfig string) (v1.PodInterface, v1.ServiceInterface, error) {
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, nil, err
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, nil, err
	}

	podClient := clientset.CoreV1().Pods(corev1.NamespaceDefault)
	serviceClient := clientset.CoreV1().Services(corev1.NamespaceDefault)
	return podClient, serviceClient, nil
}

func CreatePod(podClient v1.PodInterface, podName, buildId string) (*corev1.Pod, error) {
	isPrivileged := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
			Labels: map[string]string{
				"build-id": buildId,
			},
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "docker-dind",
					Image: common.Assembling.DockerDaemonImage,
					// Reservation for Args
					Args: []string{},
					SecurityContext: &corev1.SecurityContext{
						Privileged: &isPrivileged,
					},
				},
			},
			// Reservation for NodeSelector
			NodeSelector: map[string]string{},
		},
	}

	// Create pod
	_, err := podClient.Create(pod)
	if err != nil {
		return nil, err
	}

	// Monit pod creation status in a ticker, since the monitoring API in the k8s client is too complicated and lack of docs
	var buildPod *corev1.Pod
	var e error
	start := time.Now()
	for {
		buildPod, e = podClient.Get(podName, metav1.GetOptions{})
		if e != nil || buildPod.Status.Phase == "Running" {
			break
		}
		time.Sleep(time.Second)
		if time.Since(start).Seconds() > 30 {
			buildPod, e = nil, fmt.Errorf("Pod creation timeout")
			break
		}
	}

	return buildPod, e
}

func DeletePod(podClient v1.PodInterface, podName string) error {
	deletePolicy := metav1.DeletePropagationForeground
	if err := podClient.Delete(podName, &metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}); err != nil {
		return err
	}
	return nil
}

func InitDockerCli(registryHost string) (context.Context, *client.Client) {
	ctx := context.Background()
	var httpClient *http.Client
	buildClientHeaders := map[string]string{"Content-Type": "application/tar"}

	targetUrl := fmt.Sprintf("http://%s", registryHost)
	cli, err := client.NewClient(targetUrl, "v1.27", httpClient, buildClientHeaders)
	if err != nil {
		panic(err)
	}

	return ctx, cli
}

func CreateTarFile(dockerfile io.Reader) (io.Reader, error) {
	// Create a new tar archive.
	tarBuf := new(bytes.Buffer)
	tw := tar.NewWriter(tarBuf)

	// Add dockerfile to the archive.
	contentBuf := new(bytes.Buffer)
	contentBuf.ReadFrom(dockerfile)
	contentBytes := contentBuf.Bytes()

	hdr := &tar.Header{
		Name: "Dockerfile",
		Mode: 0600,
		Size: int64(len(contentBytes)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		log.Fatalln(err)
		return nil, err
	}
	_, err := tw.Write(contentBytes)
	if err != nil {
		return nil, err
	}
	tw.Close()

	return bytes.NewReader(tarBuf.Bytes()), nil
}

func BuildImage(ctx context.Context, cli *client.Client, host, namespace, imageName, tag string, tarFileReader io.Reader) error {
	targetTag := fmt.Sprintf("%s/%s/%s:%s", host, namespace, imageName, tag)
	buildOptions := types.ImageBuildOptions{
		Tags: []string{targetTag},
	}

	out, err := cli.ImageBuild(ctx, tarFileReader, buildOptions)
	if err != nil {
		return err
	}

	defer out.Body.Close()
	io.Copy(ioutil.Discard, out.Body)
	// io.Copy(os.Stdout, out.Body)
	return nil
}

func PushImage(ctx context.Context, cli *client.Client, host, namespace, imageName, tag, authStr string) error {
	imagePushOptions := types.ImagePushOptions{
		RegistryAuth: authStr,
	}
	targetTag := fmt.Sprintf("%s/%s/%s:%s", host, namespace, imageName, tag)

	pushResult, err := cli.ImagePush(ctx, targetTag, imagePushOptions)
	if err != nil {
		return err
	}

	defer pushResult.Close()
	io.Copy(ioutil.Discard, pushResult)
	// io.Copy(os.Stdout, pushResult)
	return nil
}

func CreateNodeBalancer(serviceClient v1.ServiceInterface, serviceName, buildId string) (*corev1.Service, error) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name: serviceName,
		},
		Spec: corev1.ServiceSpec{
			Type: corev1.ServiceTypeLoadBalancer,
			Ports: []corev1.ServicePort{
				corev1.ServicePort{
					Port: 2375,
				},
			},
			Selector: map[string]string{
				"build-id": buildId,
			},
		},
	}

	// Create NodeBalancer
	_, err := serviceClient.Create(svc)
	if err != nil {
		return nil, err
	}

	var nodeBalancer *corev1.Service
	var e error
	start := time.Now()

	for {
		nodeBalancer, e = serviceClient.Get(serviceName, metav1.GetOptions{})
		if e != nil || len(nodeBalancer.Status.LoadBalancer.Ingress) != 0 {
			break
		}
		time.Sleep(time.Second * 3)
		if time.Since(start).Seconds() > 180 {
			nodeBalancer, e = nil, fmt.Errorf("NodeBalancer creation timeout")
			break
		}
	}

	return nodeBalancer, nil
}

func DeleteNodeBalancer(serviceClient v1.ServiceInterface, serviceName string) error {
	deletePolicy := metav1.DeletePropagationForeground
	if err := serviceClient.Delete(serviceName, &metav1.DeleteOptions{
		PropagationPolicy: &deletePolicy,
	}); err != nil {
		return err
	}
	return nil
}
