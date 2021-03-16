package main

import (
	"fmt"
	"log"

	"github.com/cloudfoundry-community/gautocloud"
	"github.com/labstack/echo/v4"
	"github.com/philips-software/gautocloud-connectors/hsdp"
)

func main() {
	clients, err := gautocloud.GetAll("hsdp:iron-client")
	if err != nil {
		fmt.Printf("no iron services found: %v\n", err)
	}
	fmt.Printf("found %d client(s)\n", len(clients))

	for _, c := range clients {
		client, ok := c.(*hsdp.IronClient)
		if !ok {
			fmt.Printf("invalid client: %q\n", c)
			return
		}
		codes, _, err := client.Codes.GetCodes()
		if err != nil {
			fmt.Printf("error getting codes: %v\n", err)
			continue
		}
		for _, c := range *codes {
			fmt.Printf("code: %s:%s -> %s\n", c.ID, c.Name, c.Image)
		}
	}

	e := echo.New()

	log.Fatal(e.Start(":8080"))
}
