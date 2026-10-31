import { vi } from 'vitest'

/** Test double for the browser WebSocket: records sent frames and lets a test play the server. */
export class MockWebSocket {
  static OPEN = 1
  static CLOSED = 3
  static instances: MockWebSocket[] = []
  static install(): void {
    MockWebSocket.instances = []
    vi.stubGlobal('WebSocket', MockWebSocket)
  }
  static get last(): MockWebSocket {
    return MockWebSocket.instances[MockWebSocket.instances.length - 1]
  }

  url: string
  binaryType = 'blob'
  readyState = 0
  sent: (string | Uint8Array | ArrayBuffer)[] = []
  closedWith?: number
  private listeners: Record<string, ((event: never) => void)[]> = {}

  constructor(url: string) {
    this.url = url
    MockWebSocket.instances.push(this)
    queueMicrotask(() => {
      this.readyState = MockWebSocket.OPEN
      this.emit('open', {})
    })
  }

  addEventListener(type: string, listener: (event: never) => void): void {
    ;(this.listeners[type] ??= []).push(listener)
  }

  send(data: string | Uint8Array | ArrayBuffer): void {
    this.sent.push(data)
  }

  close(code = 1000): void {
    this.closedWith = code
    this.readyState = MockWebSocket.CLOSED
  }

  private emit(type: string, event: object): void {
    for (const listener of this.listeners[type] ?? []) listener(event as never)
  }

  serverText(frame: object): void {
    this.emit('message', { data: JSON.stringify(frame) })
  }

  serverBinary(bytes: number[]): void {
    this.emit('message', { data: new Uint8Array(bytes).buffer })
  }

  serverClose(code: number): void {
    this.readyState = MockWebSocket.CLOSED
    this.emit('close', { code })
  }
}
