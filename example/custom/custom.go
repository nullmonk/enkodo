package main

import (
    "fmt"
    "github.com/nullmonk/enkodo"
)

type CustomObj struct {
	Name string
}

func (c *CustomObj) MarshalEnkodo(enc *enkodo.Encoder) error {
    return enc.String(c.Name)
}

func (c *CustomObj) UnmarshalEnkodo(dec *enkodo.Decoder) error {
    name, err := dec.String()
    if err != nil {
        return err
    }
    c.Name = name
    return nil
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
	// Original way: pointer to struct that implements Encodee/Decodee
	Original *CustomObj `enkodo:""`
	// Proposed: Type (string), Encode func (Parent.GetName()), Decode func (DecodeCustomObj(v))
	Parent *CustomObj `enkodo:"string,.GetName(),DecodeCustomObj"`
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
