package router

func qualifiedToolName(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "." + name
}

