// Utilitário que gera uma chave AES-256 aleatória e imprime na tela.
// Uso: go run ./scripts/genkey
// Cole o output em ENCRYPTION_KEY no seu .env
package main

import (
	"fmt"
	"os"

	"github.com/ecsistem/convtrack/internal/crypto"
)

func main() {
	key, err := crypto.GenerateKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "erro: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ENCRYPTION_KEY=%s\n", key)
}
