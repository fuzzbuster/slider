package server

import (
	"fmt"
	"sort"
	"text/tabwriter"
)

const (
	// Console Basic Commands
	bgCmd     = "bg"
	bgDesc    = "Puts Console into background and returns to logging output"
	exitCmd   = "exit"
	exitDesc  = "Exits Console and terminates the Server"
	helpCmd   = "help"
	helpDesc  = "Shows this output"
	clearCmd  = "clear"
	clearDesc = "Clears the console"
)

// BgCommand implements the 'bg' command
type BgCommand struct{ BaseCommand }

func (c *BgCommand) Name() string        { return bgCmd }
func (c *BgCommand) Description() string { return bgDesc }
func (c *BgCommand) Usage() string       { return bgCmd }
func (c *BgCommand) Run(ctx *ExecutionContext, _ []string) error {
	ctx.UI().PrintlnGreyOut("Logging...")
	return ErrBackgroundConsole
}

// ExitCommand implements the 'exit' command
type ExitCommand struct{ BaseCommand }

func (c *ExitCommand) Name() string        { return exitCmd }
func (c *ExitCommand) Description() string { return exitDesc }
func (c *ExitCommand) Usage() string       { return exitCmd }
func (c *ExitCommand) Run(_ *ExecutionContext, _ []string) error {
	return ErrExitConsole
}

// HelpCommand implements the 'help' command
type HelpCommand struct{ BaseCommand }

func (c *HelpCommand) Name() string        { return helpCmd }
func (c *HelpCommand) Description() string { return helpDesc }
func (c *HelpCommand) Usage() string       { return helpCmd }
func (c *HelpCommand) Run(ctx *ExecutionContext, _ []string) error {
	svr := ctx.getServer()
	ui := ctx.UI()

	tw := new(tabwriter.Writer)
	tw.Init(ui.Writer(), 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(tw, "\n\tCommand\tDescription\t")
	_, _ = fmt.Fprintf(tw, "\n\t-------\t-----------\t\n")

	// Get primary commands with their aliases
	primaryCommands := svr.commandRegistry.GetPrimaryCommands()

	// Create a sorted list of primary command names
	var cmdNames []string
	for cmdName := range primaryCommands {
		cmdNames = append(cmdNames, cmdName)
	}
	sort.Strings(cmdNames)

	for _, cmdName := range cmdNames {
		if cmd, ok := svr.commandRegistry.Get(cmdName); ok {
			_, _ = fmt.Fprintf(tw, "\t%s\t%s\t\n", cmdName, cmd.Description())
		}
	}
	_, _ = fmt.Fprintf(tw, "\t%s\t%s\t\n", "!command", "Execute \"command\" in local shell (non-interactive)")
	_, _ = fmt.Fprintln(tw)
	_ = tw.Flush()
	return nil
}

type ClearCommand struct{ BaseCommand }

func (c *ClearCommand) Name() string        { return clearCmd }
func (c *ClearCommand) Description() string { return clearDesc }
func (c *ClearCommand) Usage() string       { return clearCmd }
func (c *ClearCommand) Run(ctx *ExecutionContext, _ []string) error {
	ctx.UI().clearScreen()
	return nil
}
