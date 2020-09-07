/*
Copyright 2019 Sorbonne Université

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

package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	yaml "gopkg.in/yaml.v2"
	"k8s.io/cli-runtime/pkg/genericclioptions"
	"k8s.io/client-go/tools/clientcmd"
	cmdconfig "k8s.io/kubernetes/pkg/kubectl/cmd/config"
	cmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
)

// A part of the general structure of a kubeconfig file
type clusterDetails struct {
	CA     []byte `json:"certificate-authority-data"`
	Server string `json:"server"`
}
type clusters struct {
	Cluster clusterDetails `json:"cluster"`
	Name    string         `json:"name"`
}
type contextDetails struct {
	Cluster string `json:"cluster"`
	User    string `json:"user"`
}
type contexts struct {
	Context contextDetails `json:"context"`
	Name    string         `json:"name"`
}
type configView struct {
	Clusters       []clusters `json:"clusters"`
	Contexts       []contexts `json:"contexts"`
	CurrentContext string     `json:"current-context"`
}

// Structure of Namecheap access credentials
type namecheap struct {
	App      string `yaml:"app"`
	APIUser  string `yaml:"apiUser"`
	APIToken string `yaml:"apiToken"`
	Username string `yaml:"username"`
}

// This reads the kubeconfig file by admin context and returns it in json format.
func getConfigView() (string, error) {
	pathOptions := clientcmd.NewDefaultPathOptions()
	streamsIn := &bytes.Buffer{}
	streamsOut := &bytes.Buffer{}
	streamsErrOut := &bytes.Buffer{}
	streams := genericclioptions.IOStreams{
		In:     streamsIn,
		Out:    streamsOut,
		ErrOut: streamsErrOut,
	}

	configCmd := cmdconfig.NewCmdConfigView(cmdutil.NewFactory(genericclioptions.NewConfigFlags(false)), streams, pathOptions)
	// "context" is a global flag, inherited from base kubectl command in the real world
	configCmd.Flags().String("context", "kubernetes-admin@kubernetes", "The name of the kubeconfig context to use")
	configCmd.Flags().Parse([]string{"--minify", "--output=json", "--raw=true"})
	if err := configCmd.Execute(); err != nil {
		log.Printf("unexpected error executing command: %v", err)
		return "", err
	}

	output := fmt.Sprint(streams.Out)
	return output, nil
}

// GetClusterServerOfCurrentContext provides cluster and server info of the current context
func GetClusterServerOfCurrentContext() (string, string, []byte, error) {
	configStr, err := getConfigView()
	if err != nil {
		log.Printf("unexpected error executing command: %v", err)
		return "", "", nil, err
	}
	var configViewDet configView
	err = json.Unmarshal([]byte(configStr), &configViewDet)
	if err != nil {
		log.Printf("unexpected error executing command: %v", err)
		return "", "", nil, err
	}

	currentContext := configViewDet.CurrentContext
	var cluster string
	for _, contextRaw := range configViewDet.Contexts {
		if contextRaw.Name == currentContext {
			cluster = contextRaw.Context.Cluster

		}
	}
	var server string
	var CA []byte
	for _, clusterRaw := range configViewDet.Clusters {
		if clusterRaw.Name == cluster {
			server = clusterRaw.Cluster.Server
			CA = clusterRaw.Cluster.CA
		}
	}
	return cluster, server, CA, nil
}

// GetServerOfCurrentContext provides the server info of the current context
func GetServerOfCurrentContext() (string, error) {
	configStr, err := getConfigView()
	if err != nil {
		log.Printf("unexpected error executing command: %v", err)
		return "", err
	}
	var configViewDet configView
	err = json.Unmarshal([]byte(configStr), &configViewDet)
	if err != nil {
		log.Printf("unexpected error executing command: %v", err)
		return "", err
	}
	currentContext := configViewDet.CurrentContext
	var cluster string
	for _, contextRaw := range configViewDet.Contexts {
		if contextRaw.Name == currentContext {
			cluster = contextRaw.Context.Cluster
		}
	}
	var server string
	for _, clusterRaw := range configViewDet.Clusters {
		if clusterRaw.Name == cluster {
			server = clusterRaw.Cluster.Server
		}
	}
	return server, nil
}

// GetNamecheapCredentials provides authentication info to have API Access
func GetNamecheapCredentials() (string, string, string, error) {
	// The path of the yaml config file of namecheap
	file, err := os.Open("../../configs/namecheap.yaml")
	if err != nil {
		log.Printf("unexpected error executing command: %v", err)
		return "", "", "", err
	}

	decoder := yaml.NewDecoder(file)
	var namecheap namecheap
	err = decoder.Decode(&namecheap)
	if err != nil {
		log.Printf("unexpected error executing command: %v", err)
		return "", "", "", err
	}
	return namecheap.APIUser, namecheap.APIToken, namecheap.Username, nil
}

// GenerateRandomString to have a unique code
func GenerateRandomString(n int) string {
	var letter = []rune("abcdefghijklmnopqrstuvwxyz0123456789")

	b := make([]rune, n)
	rand.Seed(time.Now().UnixNano())
	for i := range b {
		b[i] = letter[rand.Intn(len(letter))]
	}
	return string(b)
}

// Return whether slice contains value
func Contains(slice []string, value string) bool {
	for _, ele := range slice {
		if value == ele {
			return true
		}
	}
	return false
}

// Assert fails the test if the condition is false.
func Assert(tb testing.TB, condition bool, msg string, v ...interface{}) {
	if !condition {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\033[31m%s:%d: "+msg+"\033[39m\n\n", append([]interface{}{filepath.Base(file), line}, v...)...)
		//tb.FailNow()
	}
}

// OK fails the test if an err is not nil.
func OK(tb testing.TB, err error) {
	if err != nil {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\033[31m%s:%d: unexpected error: %s\033[39m\n\n", filepath.Base(file), line, err.Error())
		//tb.FailNow()
	}
}

// Equals fails the test if exp is not equal to act.
func Equals(tb testing.TB, exp, act interface{}) {
	if !reflect.DeepEqual(exp, act) {
		_, file, line, _ := runtime.Caller(1)
		fmt.Printf("\033[31m%s:%d:\n\n\texp: %#v\n\n\tgot: %#v\033[39m\n\n", filepath.Base(file), line, exp, act)
		//tb.FailNow()
	}
}