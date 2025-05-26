package main

import (
	"fmt"
	"os"

	p "github.com/hookenz/goose/pkg"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: goose <package[@version]> [...]")
		return
	}

	for _, arg := range os.Args[1:] {
		pkg, err := p.Parse(arg)
		if err != nil {
			fmt.Printf("Error parsing package '%s': %v\n", arg, err)
			continue
		}

		err = p.Install(pkg)
		if err != nil {
			fmt.Printf("Error installing package '%s': %v\n", pkg.Name, err)
		}
	}

	fmt.Println("All packages installed.")
}
