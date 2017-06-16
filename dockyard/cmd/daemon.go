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

package cmd

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/spf13/cobra"
	"gopkg.in/macaron.v1"

	"github.com/Huawei/containerops/common/utils"
	"github.com/Huawei/containerops/dockyard/model"
	"github.com/Huawei/containerops/dockyard/setting"
	"github.com/Huawei/containerops/dockyard/web"
)

var addressOption string
var portOption int

// webCmd is sub command which start/stop/monitor Dockyard's REST API daemon.
var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Web sub command start/stop/monitor Dockyard's REST API daemon.",
	Long:  ``,
}

// start Dockyard deamon sub command
var startDaemonCmd = &cobra.Command{
	Use:   "start",
	Short: "Start Dockyard's REST API daemon.",
	Long:  ``,
	Run:   startDeamon,
}

// stop Dockyard deamon sub command
var stopDaemonCmd = &cobra.Command{
	Use:   "stop",
	Short: "stop Dockyard's REST API daemon.",
	Long:  ``,
	Run:   stopDaemon,
}

// monitor Dockyard deamon sub command
var monitorDeamonCmd = &cobra.Command{
	Use:   "monitor",
	Short: "monitor Dockyard's REST API daemon.",
	Long:  ``,
	Run:   monitorDaemon,
}

// init()
func init() {
	RootCmd.AddCommand(daemonCmd)

	// Add start sub command
	daemonCmd.AddCommand(startDaemonCmd)
	startDaemonCmd.Flags().StringVarP(&addressOption, "address", "a", "", "http or https listen address.")
	startDaemonCmd.Flags().IntVarP(&portOption, "port", "p", 0, "the port of http.")
	startDaemonCmd.Flags().StringVarP(&configFilePath, "config", "c", "./conf/runtime.conf", "path of the config file.")

	// Add stop sub command
	daemonCmd.AddCommand(stopDaemonCmd)
	// Add daemon sub command
	daemonCmd.AddCommand(monitorDeamonCmd)
}

// startDeamon() start Dockyard's REST API daemon.
func startDeamon(cmd *cobra.Command, args []string) {
	if err := setting.SetConfig(configFilePath); err != nil {
		log.Fatalf("Failed to init settings: %s", err.Error())
		os.Exit(1)
	}

	runtimeFolder := createRuntimeFolder()
	pid := fmt.Sprintf("%d", os.Getpid())
	pidPath := fmt.Sprintf("%s/dockyard.pid", runtimeFolder)
	ioutil.WriteFile(pidPath, []byte(pid), os.ModePerm)

	model.OpenDatabase(&setting.Database)
	m := macaron.New()

	// Set Macaron Web Middleware And Routers
	web.SetDockyardMacaron(m)

	var server *http.Server
	stopChan := make(chan os.Signal)

	signal.Notify(stopChan, os.Interrupt)

	address := setting.Web.Address
	if addressOption != "" {
		address = addressOption
	}
	port := setting.Web.Port
	if portOption != 0 {
		port = portOption
	}

	go func() {
		switch setting.Web.Mode {
		case "https":
			listenaddr := fmt.Sprintf("%s:%d", address, port)
			server = &http.Server{Addr: listenaddr, TLSConfig: &tls.Config{MinVersion: tls.VersionTLS10}, Handler: m}
			if err := server.ListenAndServeTLS(setting.Web.Cert, setting.Web.Key); err != nil {
				log.Errorf("Start Dockyard https service error: %s\n", err.Error())
			}

			break
		case "unix":
			listenaddr := fmt.Sprintf("%s", address)
			if utils.IsFileExist(listenaddr) {
				os.Remove(listenaddr)
			}

			if listener, err := net.Listen("unix", listenaddr); err != nil {
				log.Errorf("Start Dockyard unix socket error: %s\n", err.Error())
			} else {
				server = &http.Server{Handler: m}
				if err := server.Serve(listener); err != nil {
					log.Errorf("Start Dockyard unix socket error: %s\n", err.Error())
				}
			}
			break
		default:
			log.Fatalf("Invalid listen mode: %s\n", setting.Web.Mode)
			os.Exit(1)
			break
		}
	}()

	// Graceful shutdown
	<-stopChan // wait for SIGINT
	log.Errorln("Shutting down server...")

	if server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(ctx)
	}

	log.Errorln("Server gracefully stopped")
}

func createRuntimeFolder() string {
	home := os.Getenv("HOME")
	containeropsRuntimeFolder := fmt.Sprintf("%s/.containerops/run/", home)
	if err := os.MkdirAll(containeropsRuntimeFolder, 0700); err != nil {
		log.Warnf("Failed to create cotainerops runtime folder: %s", containeropsRuntimeFolder)
	}
	return containeropsRuntimeFolder
}

// stopDaemon() stop Dockyard's REST API daemon.
func stopDaemon(cmd *cobra.Command, args []string) {
	home := os.Getenv("HOME")
	pidFilePath := fmt.Sprintf("%s/.containerops/run/dockyard.pid", home)

	bs, err := ioutil.ReadFile(pidFilePath)
	if err != nil {
		log.Warnf("Failed to get pid file from %s: %s", pidFilePath, err.Error())
		// Try get the pid by 'ps -ef'
		// args := []string{"-ef", "|", "grep", "dockyard"}
		pscmd := "ps -ef | grep 'dockyard.*daemon start' | grep -v grep | awk '{print $2}'"
		cmd := exec.Command("sh", "-c", pscmd)
		bs, err := cmd.Output()
		if err != nil {
			log.Errorf("Failed to get pid by ps command: %s", err.Error())
			return
		}
		pidStr := strings.TrimRight(string(bs), "\n")
		if pidStr == "" {
			log.Error("Dockyard process not found")
			return
		}

		pid, _ := strconv.Atoi(pidStr)
		fmt.Printf("shall we kill the process:%d? (y/n)", pid)
		// Read from command line
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		if input == "\n" || input == "y\n" || input == "Y\n" {
			killCmd := exec.Command("kill", "-2", pidStr)
			if _, err := killCmd.Output(); err != nil {
				fmt.Printf("Failed to kill %s: %s", pidStr, err.Error())
			} else {
				fmt.Println("Interrupt signal sent to dockyard daemon")
			}
		}

		return
	}

	pid, err := strconv.Atoi(string(bs))
	if err != nil {
		log.Errorf("Failed to parse pid: %s", string(bs))
		return
	}
	if err := syscall.Kill(pid, syscall.SIGINT); err != nil {
		log.Errorf("Failed to kill %d: %s", pid, err)
	} else {
		fmt.Println("Interrupt signal sent to dockyard daemon")
	}
}

// monitordAemon() monitor Dockyard's REST API deamon.
func monitorDaemon(cmd *cobra.Command, args []string) {

}
