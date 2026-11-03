/** Display aliases only; stored protocol names and core configuration stay intact. */
export function nodeProtocolLabel(protocol: string) {
  return (
    {
      shadowsocks: 'SS',
      shadowsocksr: 'SSR',
      hysteria2: 'HY2',
      hysteria: 'HY',
      wireguard: 'WG',
      vmess: 'VMess',
      vless: 'VLESS',
      trojan: 'Trojan',
      anytls: 'AnyTLS',
      socks5: 'SOCKS5',
      tuic: 'TUIC',
      naive: 'Naive',
    }[protocol.toLowerCase()] || protocol
  )
}
