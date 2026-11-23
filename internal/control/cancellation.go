package control

const CancelRead = "request.cancel"

func CancellableRead(method string) bool {
	return ReadOnly(method) || method == "watch" || method == "attachment.read" || method == "terminal.read" || method == "terminal.profiles" || method == "terminal.closeStatus"
}
