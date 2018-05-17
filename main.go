package main

import "github.com/QubitProducts/iris/cmd"

func main() {
	commands := cmd.GetCommands()
	commands.Execute()
}
