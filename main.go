package main

import (
	"flag"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/reflow/wrap"
	// "golang.org/x/sys/windows"
)

// func pollConsoleSize(p *tea.Program) {
// 	var lastCols, lastRows int
// 	for {
// 		cols, rows := getConsoleSize()
// 		if cols != lastCols || rows != lastRows {
// 			p.Send(ResizeMsg{
// 				cols: cols,
// 				rows: rows,
// 			})
// 			lastCols = cols
// 			lastRows = rows
// 		}
// 		time.Sleep(200 * time.Millisecond) // maybe adjust polling interval
// 	}
// }

// func getConsoleSize() (cols, rows int) {
// 	var info windows.ConsoleScreenBufferInfo
// 	handle := windows.Handle(os.Stdout.Fd())
// 	err := windows.GetConsoleScreenBufferInfo(handle, &info)
// 	if err == nil {
// 		cols = int(info.Size.X)
// 		rows = int(info.Size.Y)
// 	}
// 	return
// }

type Message struct {
	time      time.Time
	ip        string
	port      int
	text      string
	delivered bool
}

type (
	Response Message
	Ping     Message
)

// type Debug struct{ 
// 	label	string
// 	value	any
// }

type Model struct {
	mu   sync.Mutex    // Protects concurrent access to messages
	done chan struct{} // Signals shutdown to background goroutines

	sub          chan Response // Channel for receiving message notifications
	pingSub      chan Ping
	lastPingTime *time.Time

	conn          *net.UDPConn
	remoteAddr       *net.UDPAddr
	localPort        int

	whoamiServerAddrs []*net.UDPAddr

	messages  []Message
	
	textInputBuffer string
	textInput textinput.Model
	hoveredMessageIndex int

	// rows int
	// cols int

	// debug []Debug
}

// type ResizeMsg struct {
// 	rows int
// 	cols int
// }

var helpText = `Commands:
	/addwhoami <addr> [...<addr>] Add whoami server addresses
	/getaddr		Get public ip:port pair
	/quit, /q		Quit application
	/help, /h, 		Show this help
	<arrow-up>		Move up in chat history 
	<arrow-down>	Move down in chat history` 

var (
	bubblePinkAccentStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	width                 = 80
	punchInterval         = 500 * time.Millisecond
)

// addrList satisfies flag.Value interface for repeated flags
type addrList []string
func (a *addrList) String() string {
	return strings.Join(*a, ", ")
}
func (a *addrList) Set(value string) error {
	*a = append(*a, value)
	return nil
}

func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func punchHoles(conn *net.UDPConn, remoteAddr *net.UDPAddr, done chan struct{}) {
	ticker := time.NewTicker(punchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			_, err := conn.WriteToUDP([]byte("ping"), remoteAddr)
			if err != nil {
				// Silently continue on ping errors
				continue
			}
		}
	}
}

// A command to send a message to the remote peer
func sendMessage(conn *net.UDPConn, remoteAddr *net.UDPAddr, message string) tea.Cmd {
	return func() tea.Msg {
		_, _ = conn.WriteToUDP([]byte(message), remoteAddr)
		return nil
	}
}

// A command to listen for messages on our local port
func listenForMessages(sub chan<- Response, pingSub chan<- Ping, conn *net.UDPConn, done <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		buffer := make([]byte, 1024)
		for {
			select {
			case <-done:
				// stop listening
				return nil
			default:
				conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				n, addr, err := conn.ReadFromUDP(buffer)
				if err != nil {
					// try again
					continue
				}

				if string(buffer[:n]) == "ping" {
					pingSub <- Ping(Message{
						time: time.Now(),
						ip:   addr.IP.String(),
						port: addr.Port,
						text: string(buffer[:n]),
					})
				} else {
					sub <- Response(Message{
						time: time.Now(),
						ip:   addr.IP.String(),
						port: addr.Port,
						text: string(buffer[:n]),
					})
				}
			}
		}
	}
}

// A command that waits for messages on a channel.
func waitForMessages(sub <-chan Response) tea.Cmd {
	return func() tea.Msg {
		return <-sub
	}
}

// A command that waits for pings on a channel.
func waitForPings(sub <-chan Ping) tea.Cmd {
	return func() tea.Msg {
		return <-sub
	}
}

// A command to request the whoami server for our external address
func requestAddress(conn *net.UDPConn, whoamiServerAddrs []*net.UDPAddr) tea.Cmd {
	return func() tea.Msg {
		for _, addr := range whoamiServerAddrs {
			conn.WriteToUDP([]byte("whoami"), addr)
		}
		return nil
	}
}

// down = +1, up = -1
func updateInputFromHover(m *Model, direction int) {
	if len(m.messages) > 0 {
		m.hoveredMessageIndex = clamp(m.hoveredMessageIndex+direction, 0, len(m.messages))
	}
	if m.hoveredMessageIndex < len(m.messages) {
		m.textInput.SetValue(m.messages[m.hoveredMessageIndex].text)
	} else {
		m.textInput.SetValue(m.textInputBuffer)
	}			
	m.textInput.SetCursor(len(m.textInput.Value()))
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(
		listenForMessages(m.sub, m.pingSub, m.conn, m.done),
		waitForMessages(m.sub),
		waitForPings(m.pingSub),
	)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyDown:
			updateInputFromHover(m, 1)
			return m, nil

		case tea.KeyUp:
			updateInputFromHover(m, -1)
			return m, nil

		case tea.KeyEnter:
			input := m.textInput.Value()
			m.textInput.Reset()

			// enter adds whoami server(s)
			if strings.HasPrefix(input, "/addwhoami") {
				addrs := strings.Fields(input)[1:]
				for _, s := range addrs {
					addr, err := net.ResolveUDPAddr("udp", s)
					if err == nil {
						addrExists := slices.ContainsFunc(m.whoamiServerAddrs, func(a *net.UDPAddr) bool {
							return a.IP.Equal(addr.IP) && a.Port == addr.Port
						})
						if addrExists {
							return m, nil
						}
						m.whoamiServerAddrs = append(m.whoamiServerAddrs, addr)
					} else {
						m.mu.Lock()
						m.messages = append(m.messages, Message{
							time:      time.Now(),
							ip:        bubblePinkAccentStyle.Render("(SYSTEM)") + " localhost",
							port:      m.localPort,
							text:      "Error: " + err.Error(),
						})
						m.mu.Unlock()
						m.hoveredMessageIndex++
					}
				}
				return m, nil
			}

			switch input {
			// enter does nothing
			case "":
				return m, nil
			// enter prints help text
			case "/h":
				fallthrough
			case "/help":
				m.textInput.Reset()
				m.mu.Lock()
				m.messages = append(m.messages, Message{
					time:      time.Now(),
					ip:        bubblePinkAccentStyle.Render("(SYSTEM)") + " localhost",
					port:      m.localPort,
					text:      helpText,
				})
				m.mu.Unlock()
				m.hoveredMessageIndex++
				return m, nil
			// enter quits application
			case "/q":
				fallthrough
			case "/quit":
				close(m.done)
				return m, tea.Quit
			// enter sends request for our external address
			case "/getaddr":
				m.mu.Lock()
				m.messages = append(m.messages, Message{
					time:      time.Now(),
					ip:        bubblePinkAccentStyle.Render("(You)") + " localhost",
					port:      m.localPort,
					text:      input,
					delivered: false,
				})
				m.mu.Unlock()
				m.hoveredMessageIndex++
				return m, requestAddress(m.conn, m.whoamiServerAddrs)
			// enter appends message to local chat and sends that message to peer
			default:
				var delivered bool
				if m.lastPingTime != nil {
					delivered = time.Since(*m.lastPingTime) <= punchInterval
				}
				m.mu.Lock()
				m.messages = append(m.messages, Message{
					time:      time.Now(),
					ip:        bubblePinkAccentStyle.Render("(You)") + " localhost",
					port:      m.localPort,
					text:      input,
					delivered: delivered,
				})
				m.mu.Unlock()
				m.hoveredMessageIndex++
				return m, sendMessage(m.conn, m.remoteAddr, input)
			}

		case tea.KeyCtrlC:
			close(m.done)
			return m, tea.Quit

		// Handle regular typing
		default:
			m.hoveredMessageIndex = len(m.messages)
			var cmd tea.Cmd
			m.textInput, cmd = m.textInput.Update(msg)
			m.textInputBuffer = m.textInput.Value()
			return m, cmd
		}

	// Handle incoming peer messages
	case Response:
		// m.hoveredMessageIndex++

		if strings.HasPrefix(msg.text, "addr:") {
			var addr string
			_, _ = fmt.Sscanf(msg.text, "addr:%s", &addr)
			msg = Response{
				time: msg.time,
				ip:   bubblePinkAccentStyle.Render("(WHOAMI)") + " " + msg.ip,
				port: msg.port,
				text: addr,
			}
		}

		m.mu.Lock()
		m.messages = append(m.messages, Message(msg))
		m.mu.Unlock()

		return m, waitForMessages(m.sub)

	case Ping:
		m.lastPingTime = &msg.time
		return m, waitForPings(m.pingSub)

	// case ResizeMsg:
	// 	m.rows = msg.rows
	// 	m.cols = msg.cols - 3 // -3 because of the "> " prompt
	// 	m.textInput.Width = m.cols
	// 	return m, nil

	// Handle any other events
	default:
		return m, nil
	}
}

func (m *Model) View() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	var output string

	// debug
	// output += fmt.Sprintf("%+v\n", m.whoamiServerAddrs)
	// output += "currentMessageIndex: " + strconv.Itoa(m.hoveredMessageIndex)
	// output += "\nhoveredMessage: " + m.hoveredMessage
	// output += "\ncopied: " + strconv.FormatBool(m.copied)
	// output += "\ntextInput.Value(): " + m.textInput.Value()
	// output += fmt.Sprintf("\nrows:%d cols:%d", m.rows, m.cols)
	// output += fmt.Sprintf("\nlast ping: %v", m.lastPingTime)
	// output += "\n\n"

	for _, message := range m.messages {
		output += fmt.Sprintf("%s:%d %s%s%s",
			message.ip,
			message.port,
			bubblePinkAccentStyle.Render("["),
			message.time.Format("15:04"),
			bubblePinkAccentStyle.Render("]"),
		)
		if message.delivered {
			output += " ✓✓"
		}
		output += "\n"
		output += wrap.String(fmt.Sprintf("%s %s\n\n", bubblePinkAccentStyle.Render("|"), message.text), width)
	}

	output += fmt.Sprintf("\n%s", m.textInput.View())

	return output
}

func main() {
	localPort := flag.Int("lport", 0, "Local port to bind to")
	remoteIP := flag.String("rip", "", "Remote IP address")
	remotePort := flag.Int("rport", 0, "Remote port")
	
	var whoamiServers addrList
	flag.Var(&whoamiServers, "whoami", "[Optional] Whoami server address (can be repeated)")

	flag.Parse()

	// Validate flags
	if *localPort == 0 || *remoteIP == "" || *remotePort == 0 {
		fmt.Println("Error: Required flags missing")
		fmt.Println("Usage:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	var whoamiAddrs []*net.UDPAddr
	for _, s := range whoamiServers {
		addr, err := net.ResolveUDPAddr("udp", s)
		if err != nil {
			fmt.Printf("Invalid whoami server address: %v\n", err)
			os.Exit(1)
		}
		whoamiAddrs = append(whoamiAddrs, addr)
	}

	localAddr := &net.UDPAddr{
		IP:   net.ParseIP("0.0.0.0"),
		Port: *localPort,
	}

	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		fmt.Printf("Failed to bind to port %d: %v\n", *localPort, err)
		os.Exit(1)
	}
	defer conn.Close()

	remoteAddr := &net.UDPAddr{
		IP:   net.ParseIP(*remoteIP),
		Port: *remotePort,
	}

	if remoteAddr.IP == nil {
		fmt.Printf("Invalid remote IP address: %s\n", *remoteIP)
		os.Exit(1)
	}

	done := make(chan struct{})

	// Start punching UDP holes in our router towards our peer
	go punchHoles(conn, remoteAddr, done)

	ti := textinput.New()
	ti.Placeholder = "Type something..."
	ti.Focus()
	ti.CharLimit = 256
	ti.Width = width

	ti.Cursor.Style = bubblePinkAccentStyle
	ti.PromptStyle = bubblePinkAccentStyle

	p := tea.NewProgram(&Model{
		done:              done,
		localPort:         *localPort,
		conn:              conn,
		remoteAddr:        remoteAddr,
		sub:               make(chan Response),
		pingSub:           make(chan Ping),
		textInput:         ti,
		whoamiServerAddrs: whoamiAddrs,
	})

	// start polling the console's rows and columns
	// go pollConsoleSize(p)

	if _, err := p.Run(); err != nil {
		fmt.Printf("Uh oh, there was an error: %v\n", err)
		os.Exit(1)
	}
}
