# P2Pc

Peer to peer chat TUI that uses UDP hole punching to achieve p2p communication

### Note

If you have multiple network interfaces on your system then the UDP hole punching
may not be succesful as I haven't accounted for that yet.

## Package dependancies:

- github.com/charmbracelet/lipgloss
- github.com/charmbracelet/bubbles/textinput
- github.com/charmbracelet/bubbletea

## Special thanks to:

- [Akilan](https://github.com/akilan1999) for introducing me to UDP hole punching
- [This brilliant write up](https://www.netmanias.com/ko/post/blog/6263/nat-network-protocol-p2p/p2p-nat-nat-traversal-technic-rfc-5128-part-2-udp-hole-punching) on NAT traversal by 유창모 (Changmo Yoo)
- The [charm.sh](https://charm.sh/) project for the brilliant TUI framework
