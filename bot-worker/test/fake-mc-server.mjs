// Deterministic fake Minecraft server for JianManager FR-043 end-to-end tests.
//
// Java 8 is the only JDK on the e2e host, so a real Paper 1.21 server can't run.
// This speaks just enough of the protocol (offline, a simple-login version) for a
// REAL mineflayer bot — spawned by the real bot-worker — to reach 'spawn', which is
// exactly what makes the bot report `connected`. Every JianManager code path stays
// real; only the "Paper" implementation is stubbed.
//
// It also doubles as a server console so the terminal e2e can drive it:
//   - prints a "Done!" startup banner (terminal sees instance startup output)
//   - on stdin: `list` -> online players, `say <msg>` -> "[Server] <msg>", `stop` -> exit
//
// Module resolution: this file lives under bot-worker/ so `minecraft-protocol`
// (a mineflayer transitive dep) resolves from bot-worker/node_modules.
//
// Usage: node fake-mc-server.mjs --port=25599 [--version=1.8.9]
import { createServer } from 'minecraft-protocol'

function arg(name, def) {
  const p = process.argv.find((a) => a.startsWith(`--${name}=`))
  return p ? p.slice(name.length + 3) : def
}

const PORT = parseInt(arg('port', '25599'), 10)
const VERSION = arg('version', '1.8.9')

/** username -> client, the live "online players" set the `list` command reports. */
const players = new Map()
let nextEntityId = 1

const server = createServer({
  'online-mode': false,
  host: '127.0.0.1',
  port: PORT,
  version: VERSION,
  motd: 'JianManager E2E fake server',
  maxPlayers: 20,
})

server.on('connection', (client) => {
  console.log(`[conn] incoming connection to fake server on ${PORT}`)
})

server.on('listening', () => {
  console.log(`Starting Minecraft server on 127.0.0.1:${PORT} (pid ${process.pid})`)
  // Mimics vanilla/Paper's readiness line so the terminal e2e can assert startup output.
  console.log(`Done! Server started on port ${PORT}, for help, type "help"`)
})

server.on('login', (client) => {
  players.set(client.username, client)
  console.log(`${client.username} joined the game (now ${players.size} online)`)

  // Minimal play-state entry that gets a real mineflayer client to 'spawn':
  // join-game (login) -> position -> first update_health(health>0). See mineflayer
  // health.js: 'spawn' is emitted on the first update_health with health>0.
  client.write('login', {
    entityId: nextEntityId++,
    levelType: 'default',
    gameMode: 0,
    dimension: 0,
    difficulty: 0,
    maxPlayers: 20,
    reducedDebugInfo: false,
  })
  client.write('position', { x: 0, y: 64, z: 0, yaw: 0, pitch: 0, flags: 0x00 })
  client.write('update_health', { health: 20, food: 20, foodSaturation: 5 })

  const drop = () => {
    if (players.delete(client.username)) {
      console.log(`${client.username} left the game`)
    }
  }
  client.on('end', drop)
  client.on('error', drop)
})

server.on('error', (err) => {
  console.log(`server error: ${err.message}`)
  // Bind conflict must be loud (instance CRASH), not a silent wrong-server join.
  if (err && (err.code === 'EADDRINUSE' || err.code === 'EACCES')) {
    process.exit(1)
  }
})

// Server console: read stdin lines and emulate the subset of console commands the
// e2e drives. Terminal stdin (browser -> CP -> Worker -> process stdin) lands here.
process.stdin.setEncoding('utf8')
let buf = ''
process.stdin.on('data', (chunk) => {
  buf += chunk
  let nl
  while ((nl = buf.indexOf('\n')) >= 0) {
    const line = buf.slice(0, nl).replace(/\r$/, '').trim()
    buf = buf.slice(nl + 1)
    if (line) handleConsole(line)
  }
})

function handleConsole(line) {
  if (line === 'list') {
    const names = [...players.keys()]
    console.log(`There are ${names.length} of a max of 20 players online: ${names.join(', ')}`)
  } else if (line.startsWith('say ')) {
    console.log(`[Server] ${line.slice(4)}`)
  } else if (line === 'stop') {
    console.log('Stopping the server')
    server.close()
    process.exit(0)
  } else {
    console.log(`Unknown command. Type "help" for help.`)
  }
}

for (const sig of ['SIGTERM', 'SIGINT', 'SIGHUP']) {
  process.on(sig, () => process.exit(0))
}
