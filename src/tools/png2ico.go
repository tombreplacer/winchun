package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: go run png2ico.go <input.png> <output.ico>")
		os.Exit(1)
	}

	inputPath := os.Args[1]
	outputPath := os.Args[2]

	pngFile, err := os.Open(inputPath)
	if err != nil {
		fmt.Printf("Failed to open PNG file: %v\n", err)
		os.Exit(1)
	}
	defer pngFile.Close()

	pngInfo, err := pngFile.Stat()
	if err != nil {
		fmt.Printf("Failed to get PNG info: %v\n", err)
		os.Exit(1)
	}

	pngSize := pngInfo.Size()

	icoFile, err := os.Create(outputPath)
	if err != nil {
		fmt.Printf("Failed to create ICO file: %v\n", err)
		os.Exit(1)
	}
	defer icoFile.Close()

	// Write ICO Header (6 bytes)
	// Reserved: 2 bytes (0x0000)
	// Type: 2 bytes (1 for icon)
	// Count: 2 bytes (1 image)
	binary.Write(icoFile, binary.LittleEndian, uint16(0))
	binary.Write(icoFile, binary.LittleEndian, uint16(1))
	binary.Write(icoFile, binary.LittleEndian, uint16(1))

	// Write Directory Entry (16 bytes)
	// Width: 1 byte (0 for 256px)
	// Height: 1 byte (0 for 256px)
	// Color Count: 1 byte (0 for >=8bpp)
	// Reserved: 1 byte (0)
	// Planes: 2 bytes (1)
	// Bit Count: 2 bytes (32)
	// Bytes Size: 4 bytes (size of PNG data)
	// Bytes Offset: 4 bytes (offset to PNG data, 6 + 16 = 22 bytes)
	icoFile.Write([]byte{0}) // Width
	icoFile.Write([]byte{0}) // Height
	icoFile.Write([]byte{0}) // Color Count
	icoFile.Write([]byte{0}) // Reserved
	binary.Write(icoFile, binary.LittleEndian, uint16(1))
	binary.Write(icoFile, binary.LittleEndian, uint16(32))
	binary.Write(icoFile, binary.LittleEndian, uint32(pngSize))
	binary.Write(icoFile, binary.LittleEndian, uint32(22))

	// Copy raw PNG data into ICO file
	_, err = io.Copy(icoFile, pngFile)
	if err != nil {
		fmt.Printf("Failed to copy PNG data: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Successfully converted %s to %s (%d bytes)\n", inputPath, outputPath, pngSize+22)
}
