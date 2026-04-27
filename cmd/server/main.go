// GooseRelayVPN server (VPS exit): receives AES-encrypted frame batches from
// Apps Script, decrypts, and bridges to real upstream TCP targets.
package main

import (
	"flag"
	"log"

	"github.com/kianmhz/GooseRelayVPN/internal/config"
	"github.com/kianmhz/GooseRelayVPN/internal/exit"
)

func main() {
	configPath := flag.String("config", "server_config.json", "path to server config JSON")
	flag.Parse()

	cfg, err := config.LoadServer(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	srv, err := exit.New(exit.Config{
		ListenAddr: cfg.ListenAddr,
		AESKeyHex:  cfg.AESKeyHex,
	})
	if err != nil {
		log.Fatalf("exit: %v", err)
	}

	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("listen: %v", err)
	}
}
