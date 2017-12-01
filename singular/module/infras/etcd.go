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

package infras

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"text/template"

	"github.com/cloudflare/cfssl/cli"
	"github.com/cloudflare/cfssl/cli/genkey"
	"github.com/cloudflare/cfssl/cli/sign"
	"github.com/cloudflare/cfssl/csr"
	"github.com/cloudflare/cfssl/signer"

	"github.com/Huawei/containerops/common/utils"
	"github.com/Huawei/containerops/singular/module/objects"
	t "github.com/Huawei/containerops/singular/module/template"
	"github.com/Huawei/containerops/singular/module/tools"
)

const (
	//EtcdMinimalNodes is minimal etcd nodes number.
	EtcdMinimalNodes = 2
	//EtcdServerConfig is etcd config location in the node.
	EtcdServerConfig = "/etc/etcd"
	//EtcdServerSSL is the etcd ssl files folder name in the node.
	//Full path is /etc/etcd/ssl
	EtcdServerSSL = "ssl"
)

//EtcdEndpoint is the etcd node struct.
type EtcdEndpoint struct {
	IP    string
	Name  string
	Nodes string
}

//DeployEtcdInCluster deploy etcd cluster.
//Notes:
//  1. Only count master nodes in etcd deploy process.
//  2.
func DeployEtcdInCluster(d *objects.Deployment, infra *objects.Infra, stdout io.Writer, timestamp bool) error {
	//Check master node number.
	if infra.Master > len(d.Nodes) {
		return fmt.Errorf("deploy %s nodes more than %d", infra.Name, len(d.Nodes))
	}

	if infra.Master < EtcdMinimalNodes {
		return fmt.Errorf("etcd node no less than %d nodes", EtcdMinimalNodes)
	}

	//Init nodes, endpoints and adminEndpoints parameters.
	nodes := []*objects.Node{}
	etcdEndpoints, etcdPeerEndpoints := []string{}, []string{}

	//Get nodes from outputs of Deployment to determine etcd cluster nodes.
	//TODO Now just choose the first nodes of list. Should have a algorithm and filers determined.
	for i := 0; i < infra.Master; i++ {
		//Etcd Notes
		nodes = append(nodes, d.Nodes[i])

		//Etcd endpoints for client
		etcdEndpoints = append(etcdEndpoints,
			fmt.Sprintf("https://%s:2379", d.Outputs[fmt.Sprintf("NODE_%d", i)].(string)))

		//Etcd admin endpoints for peer
		etcdPeerEndpoints = append(etcdPeerEndpoints,
			fmt.Sprintf("%s=https://%s:2380", fmt.Sprintf("etcd-node-%d", i),
				d.Outputs[fmt.Sprintf("NODE_%d", i)].(string)))
	}

	//Deployment output
	d.Output("EtcdEndpoints", strings.Join(etcdEndpoints, ","))
	d.Output("EtcdPeerEndpoints", strings.Join(etcdPeerEndpoints, ","))

	//Infra output
	infra.Output("EtcdEndpoints", strings.Join(etcdEndpoints, ","))
	infra.Output("EtcdPeerEndpoints", strings.Join(etcdPeerEndpoints, ","))

	objects.WriteLog(d.Outputs["EtcdEndpoints"].(string), stdout, timestamp, d, infra)
	objects.WriteLog(d.Outputs["EtcdPeerEndpoints"].(string), stdout, timestamp, d, infra)

	//Generate Etcd CA/Systemd/Config Files
	if files, err := generateEtcdFiles(d.Config, nodes, strings.Join(etcdPeerEndpoints, ","), infra.Version); err != nil {
		return err
	} else {
		objects.WriteLog(fmt.Sprintf("Etcd CA/Systemd/config files: [%v]", files), stdout, timestamp, d, infra)

		//Upload Etcd files to node
		if err := uploadEtcdFiles(files, d.Tools.SSH.Private, nodes, stdout); err != nil {
			return err
		}
	}

	//Download etcd binary files in nodes.
	for _, c := range infra.Components {
		objects.WriteLog(fmt.Sprintf("Download %s in deploy notes", c.Binary), stdout, timestamp, d, infra, c)
		if err := d.DownloadBinaryFile(c.Binary, c.URL, nodes, stdout, timestamp); err != nil {
			return err
		}
	}

	//Start etcd
	if err := startEtcdCluster(d.Tools.SSH.Private, nodes, stdout); err != nil {
		return err
	}

	return nil
}

//Generate Etcd CA SSL and Systemd service Files
func generateEtcdFiles(src string, nodes []*objects.Node, etcdEndpoints string, version string) (map[string]map[string]string, error) {
	result := map[string]map[string]string{}

	//If ca file exist, remove it.
	sslBase := path.Join(src, tools.CAFilesFolder, tools.CAEtcdFolder)
	if utils.IsDirExist(sslBase) == true {
		os.RemoveAll(sslBase)
	}

	//Mkdir ssl folder
	os.MkdirAll(sslBase, os.ModePerm)

	//If service folder, remove it.
	serviceBase := path.Join(src, tools.ServiceFilesFolder, tools.ServiceEtcdFolder)
	if utils.IsDirExist(serviceBase) == true {
		os.RemoveAll(serviceBase)
	}

	//Mkdir ssl folder
	os.MkdirAll(serviceBase, os.ModePerm)

	//CA root files
	caFile := path.Join(src, tools.CAFilesFolder, tools.CARootFilesFolder, tools.CARootPemFile)
	caKeyFile := path.Join(src, tools.CAFilesFolder, tools.CARootFilesFolder, tools.CARootKeyFile)
	configFile := path.Join(src, tools.CAFilesFolder, tools.CARootFilesFolder, tools.CARootConfigFile)

	//Loop Etcd nodes and generate CA files.
	ch := make(chan error)
	for i, node := range nodes {
		go func(i int, node *objects.Node) {
			//Mkdir with node ip.
			if utils.IsDirExist(path.Join(sslBase, node.IP)) == false {
				os.MkdirAll(path.Join(sslBase, node.IP), os.ModePerm)
			}

			if utils.IsDirExist(path.Join(serviceBase, node.IP)) == false {
				os.MkdirAll(path.Join(serviceBase, node.IP), os.ModePerm)
			}

			endpoint := EtcdEndpoint{
				IP:    node.IP,
				Name:  fmt.Sprintf("etcd-node-%d", i),
				Nodes: etcdEndpoints,
			}

			//Generate Etcd SSL files
			if files, err := generateEtcdSSLFiles(caFile, caKeyFile, configFile, endpoint, version, sslBase, endpoint.IP); err != nil {
				ch <- err
				return
			} else {
				result[endpoint.IP] = files
			}

			//Generate Etcd Systemd File
			if files, err := generateEtcdServiceFile(endpoint, version, serviceBase, endpoint.IP); err != nil {
				ch <- err
				return
			} else {
				for k, v := range files {
					result[endpoint.IP][k] = v
				}
			}
			ch <- nil

		}(i, node)
	}
	for i := 0; i < len(nodes); i++ {
		if err := <-ch; err != nil {
			return result, err
		}
	}

	return result, nil
}

//generateEtcdSSLFiles generate ssl file with node information.
func generateEtcdSSLFiles(caFile, caKeyFile, configFile string, node EtcdEndpoint, version, base, ip string) (map[string]string, error) {
	var tpl bytes.Buffer
	var err error

	files := map[string]string{
		tools.CAEtcdCSRConfigFile: path.Join(base, ip, tools.CAEtcdCSRConfigFile),
		tools.CAEtcdKeyPemFile:    path.Join(base, ip, tools.CAEtcdKeyPemFile),
		tools.CAEtcdCSRFile:       path.Join(base, ip, tools.CAEtcdCSRFile),
		tools.CAEtcdPemFile:       path.Join(base, ip, tools.CAEtcdPemFile),
	}

	// Generate csr file
	sslTp := template.New("etcd-csr")
	sslTp, _ = sslTp.Parse(t.EtcdCATemplate[version])
	sslTp.Execute(&tpl, node)
	csrFileBytes := tpl.Bytes()

	req := csr.CertificateRequest{
		KeyRequest: csr.NewBasicKeyRequest(),
	}

	// Unmarshal csr to certificate request
	err = json.Unmarshal(csrFileBytes, &req)
	if err != nil {
		return files, err
	}

	// Generate key file and others.
	var key, csrBytes []byte
	g := &csr.Generator{Validator: genkey.Validator}
	csrBytes, key, err = g.ProcessRequest(&req)
	if err != nil {
		return files, err
	}

	c := cli.Config{
		CAFile:     caFile,
		CAKeyFile:  caKeyFile,
		ConfigFile: configFile,
		Profile:    "kubernetes",
		Hostname:   "",
	}

	s, err := sign.SignerFromConfig(c)
	if err != nil {
		return files, err
	}

	var cert []byte
	signReq := signer.SignRequest{
		Request: string(csrBytes),
		Hosts:   signer.SplitHosts(c.Hostname),
		Profile: c.Profile,
	}

	cert, err = s.Sign(signReq)
	if err != nil {
		return files, err
	}

	err = ioutil.WriteFile(files[tools.CAEtcdCSRConfigFile], csrFileBytes, 0600)
	err = ioutil.WriteFile(files[tools.CAEtcdKeyPemFile], key, 0600)
	err = ioutil.WriteFile(files[tools.CAEtcdCSRFile], csrBytes, 0600)
	err = ioutil.WriteFile(files[tools.CAEtcdPemFile], cert, 0600)

	if err != nil {
		return files, err
	}

	return files, nil
}

//generateEtcdServiceFile generate systemd service file
func generateEtcdServiceFile(node EtcdEndpoint, version, base, ip string) (map[string]string, error) {
	var serviceTpl bytes.Buffer
	files := map[string]string{}

	files[tools.ServiceEtcdFile] = path.Join(base, ip, tools.ServiceEtcdFile)

	serviceTp := template.New("etcd-systemd")
	serviceTp, _ = serviceTp.Parse(t.EtcdSystemdTemplate[version])
	serviceTp.Execute(&serviceTpl, node)
	serviceTpFileBytes := serviceTpl.Bytes()

	if err := ioutil.WriteFile(files[tools.ServiceEtcdFile], serviceTpFileBytes, 0700); err != nil {
		return files, err
	}

	return files, nil
}

//upload Etcd SSL files and systemd file to nodes
func uploadEtcdFiles(f map[string]map[string]string, key string, nodes []*objects.Node, stdout io.Writer) error {
	ch := make(chan error)
	for _, node := range nodes {
		go func(node *objects.Node) {
			files := []map[string]string{}

			for k, file := range f[node.IP] {
				switch k {
				case tools.ServiceEtcdFile:
					files = append(files, map[string]string{
						"src":  file,
						"dest": path.Join(tools.SystemdServerPath, tools.ServiceEtcdFile),
					})
				default:
					files = append(files, map[string]string{
						"src":  file,
						"dest": path.Join(EtcdServerConfig, EtcdServerSSL, k),
					})
				}
			}

			ch <- tools.DownloadComponent(files, node.IP, key, node.User, stdout)

		}(node)
	}
	for i := 0; i < len(nodes); i++ {
		if err := <-ch; err != nil {
			return err
		}
	}

	return nil
}

//startEtcdCluster in node must with --no-block
func startEtcdCluster(key string, nodes []*objects.Node, stdout io.Writer) error {
	commands := []string{
		"systemctl daemon-reload",
		"systemctl enable etcd",
		"systemctl start --no-block etcd",
	}

	ch := make(chan error)
	for _, node := range nodes {
		go func(node *objects.Node) {

			ch <- utils.SSHCommand(node.User, key, node.IP, 22, commands, stdout, os.Stderr)
		}(node)
	}

	for i := 0; i < len(nodes); i++ {
		if err := <-ch; err != nil {
			return err
		}
	}

	return nil
}
