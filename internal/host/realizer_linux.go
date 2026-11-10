package host

// RealizerFacts are host observations that decide whether rendered network
// files could work here. They are read-only and carry no configuration.
type RealizerFacts struct {
	// ForeignFiles are networkd files Andaru did not render, as sorted absolute paths.
	ForeignFiles []string
	PPPDaemon    bool
	PPPoEPlugin  bool
	// PPPDevice reports /dev/ppp; Debian cloud kernels ship without PPP.
	PPPDevice bool
}
