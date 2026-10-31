// Heuristic log-level detection (spec section 8). Mirrors internal/logs/levels.go regex for regex,
// including the JSON variant `level"?[=:]`; an error-class match beats a warn-class match in the same line.
const LEVEL_KEY_VALUE = /level"?[=:]\s*"?(warn|warning|error|fatal|panic|critical)/gi
const LEVEL_TOKEN = /\b(WARN|WARNING|ERROR|FATAL|PANIC|CRITICAL)\b/g
const LEVEL_BRACKET = /\[(warn|error|fatal)\]/gi

export function level(message: string): 'error' | 'warn' | '' {
  let result: 'error' | 'warn' | '' = ''
  for (const pattern of [LEVEL_KEY_VALUE, LEVEL_TOKEN, LEVEL_BRACKET]) {
    for (const match of message.matchAll(pattern)) {
      const word = match[1].toLowerCase()
      if (word !== 'warn' && word !== 'warning') return 'error'
      result = 'warn'
    }
  }
  return result
}
