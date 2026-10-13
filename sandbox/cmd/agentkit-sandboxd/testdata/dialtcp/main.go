// Command dialtcp dials the TCP address it is given, with Multipath TCP
// disabled (Landlock's TCP rules do not cover it), and exits 0 on success
// or 3 printing the error. The confined-agent tests build it into the
// workspace.
package main

import (
	"fmt"
	"net"
	"os"
)

func main() {
	var d net.Dialer
	d.SetMultipathTCP(false)
	c, err := d.Dial("tcp", os.Args[1])
	if err != nil {
		fmt.Println(err)
		os.Exit(3)
	}
	_ = c.Close()
}
