package commands

func RegisterAll(r *Registry) {
	RegisterPublic(r)
	registerExtensions(r)
}

func RegisterPublic(r *Registry) {
	// r.Register(HelpCommand())
	// Other public commands...
}
