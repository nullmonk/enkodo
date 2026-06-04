package main

import "fmt"

type CustomObj struct {
	Name string
}

func (c *CustomObj) GetName() string {
	if c == nil {
		return ""
	}
	return c.Name
}

func (c *CustomObj) SetName(name string) {
	c.Name = name
}

func DecodeCustomObj(name string) *CustomObj {
	return &CustomObj{Name: name}
}

type User struct {
	// Proposed: Type (string), Encode func (Parent.GetName()), Decode func (DecodeCustomObj(v))
	Parent *CustomObj `enkodo:"string,.GetName(),DecodeCustomObj"`
	// Test method for decoding
	OtherParent *CustomObj `enkodo:"string,.GetName(),.SetName"`
}

type Other struct {
    // Current way still works
    Age int `enkodo:""`
    // Override way still works
    Status int `enkodo:"int8"`
}

func main() {
    fmt.Println("Example compiled successfully")
}
