package main

import (
	"context"
	"fmt"

	"github.com/Huawei/containerops/singular/init_config"
	"github.com/Huawei/containerops/singular/nodes"
	"github.com/digitalocean/godo"
	"golang.org/x/oauth2"
)

type SSHCommander struct {
	User string
	IP   string
}

//var nodes = [2][2]string{{"192.168.60.141", "centos-master"}, {"192.168.60.150", "centos-minion"}}
var m = make(map[string]string)

type TokenSource struct {
	AccessToken string
}

func (t *TokenSource) Token() (*oauth2.Token, error) {
	token := &oauth2.Token{
		AccessToken: t.AccessToken,
	}
	return token, nil
}
func main() {
	// SSHCommander.IP
	m["centos-master"] = init_config.MasterIP
	//m["centos-minion"] = init_config.NodeIP
	for k, ip := range m {
		fmt.Printf("k=%v, v=%v\n", k, ip)
		if k == "centos-master" {
			nodes.Deploymaster(m, ip)
		}
		if k == "centos-minion" {
			nodes.Deploynode(m, ip)
		}
	}

	tokenSource := &TokenSource{
		AccessToken: init_config.TSpet,
	}
	oauthClient := oauth2.NewClient(oauth2.NoContext, tokenSource)
	client := godo.NewClient(oauthClient)

	dropletName := "lidian-unbantu-droplet"

	createRequest := &godo.DropletCreateRequest{
		Name:   dropletName,
		Region: "nyc3",
		Size:   "512mb",
		Image: godo.DropletCreateImage{
			Slug: "ubuntu-17-04-x64", //17.04 x64
		},
	}

	ctx := context.TODO()
	//newDroplet
	_, _, err := client.Droplets.Create(ctx, createRequest)

	fmt.Printf("%s\n\n", err)

}
