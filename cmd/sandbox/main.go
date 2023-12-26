package main

import (
	"fmt"
	"strings"
)

func getCashWithdrawals() string {
	var out strings.Builder

	for {
		// Ask the user to enter a withdrawal amount from stdin
		fmt.Println("Enter a withdrawal amount (or 0 to quit):")

		var amount int
		if _, err := fmt.Scanln(&amount); err != nil {
			panic(err)
		}

		if amount == 0 {
			break
		} else if amount < 0 {
			fmt.Println("Please enter a positive amount.")
			continue
		}

		out.WriteString(fmt.Sprintf("Took $%d\n", amount))
	}

	return out.String()
}

func main() {
	s := getCashWithdrawals()
	fmt.Println(s)
}
