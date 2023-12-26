package main

import (
	"github.com/go-pdf/fpdf"
)

func getText() string {
	return "Hey Tash!!! You do best!\nAnd you look stunning today!"
}

func main() {
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.AddPage()
	pdf.SetFont("Helvetica", "", 16)
	pdf.MultiCell(0, 10, "\n\n", "", "", false)
	pdf.MultiCell(0, 10, getText(), "", "", false)

	if err := pdf.OutputFileAndClose("hello-again.pdf"); err != nil {
		panic(err)
	}
}
