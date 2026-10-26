import { describe, expect, it } from 'vitest'
import { networkArguments } from './network-arguments'

describe('desktop network overrides', () => {
  it('forwards explicit values and false without generating defaults', () => {
    expect(networkArguments(['Astral.exe', '--user-data-dir=/tmp/example'])).toEqual([])
    expect(networkArguments(['--listen', '0.0.0.0:4545', '--public-url=', '--allow-remote-management=false'])).toEqual([
      '--listen', '0.0.0.0:4545', '--public-url=', '--allow-remote-management=false',
    ])
    expect(networkArguments(['--public-url', 'http://host.example:4545', '--allow-remote-management'])).toEqual([
      '--public-url', 'http://host.example:4545', '--allow-remote-management',
    ])
    expect(networkArguments(['--allow-remote-management', 'false', '--listen=127.0.0.1:4545'])).toEqual([
      '--allow-remote-management=false', '--listen=127.0.0.1:4545',
    ])
  })
  it('reports missing values and removed native TLS options', () => {
    for (const input of [['--listen'], ['--public-url', '--listen=x'], ['--allow-remote-management', 'off'], ['--tls-key=x'], ['--tls-cert', '/tmp/cert']]) {
      expect(() => networkArguments(input)).toThrow()
    }
  })
})
