package models

type Network struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Driver     string             `json:"driver"`
	Scope      string             `json:"scope"`
	Subnet     string             `json:"subnet"`
	Gateway    string             `json:"gateway"`
	Containers []NetworkContainer `json:"containers"`
	Labels     map[string]string  `json:"labels"`
	Created    string             `json:"created"`
}

type NetworkContainer struct {
	Name       string `json:"name"`
	IPv4       string `json:"ipv4"`
	MacAddress string `json:"mac_address"`
}
