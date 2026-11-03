import { describe, expect, it } from 'vitest'
import { nodeRegion, nodeRegionDisplayName } from './node-region'

describe('region hints from node names', () => {
  it.each([
    ['香港800 专线 BGP', 'hk'],
    ['HK 000', 'hk'],
    ['hk', 'hk'],
    ['hysteria2-hk1', 'hk'],
    ['20260629-HK', 'hk'],
    ['20260629-hk-6c4m2k04', 'hk'],
    ['Hong Kong 03', 'hk'],
    ['JP 001', 'jp'],
    ['日本621 中继', 'jp'],
    ['Tokyo 02', 'jp'],
    ['SG 011', 'sg'],
    ['狮城502 中继 动态', 'sg'],
    ['Singapore-1', 'sg'],
    ['TW001', 'tw'],
    ['台灣611', 'tw'],
    ['US 013', 'us'],
    ['USA-01', 'us'],
    ['英国751', 'gb'],
    ['UK-01', 'gb'],
    ['GB01', 'gb'],
    ['韩国755', 'kr'],
    ['俄罗斯752', 'ru'],
    ['马来753', 'my'],
    ['马来西亚753', 'my'],
    ['阿根廷756', 'ar'],
    ['土耳其757', 'tr'],
    ['印度尼西亚', 'id'],
    ['India-1', 'in'],
    ['IN 01', 'in'],
    ['🇭🇰 HK 香港', 'hk'],
    ['🇺🇸 美国635 AI', 'us'],
    ['ＨＫ ０１', 'hk'],
    ['香港专线 hk.example.com', 'hk'],
  ])('recognizes %s without resolving a server', (name, expected) => {
    expect(nodeRegion(name)?.code).toBe(expected)
  })
  it.each([
    '官网xmrth1.net',
    '剩余流量 19GB',
    'gist.github.com',
    'hk.example.com',
    'us.example.com:443',
    '官网 https://hk.example.com/',
    'vless://user@jp.example.com:443/',
    'USDT monthly',
    'bestUSnode',
    'think different',
    'private link',
    'no region',
    'node is ready',
    'it works',
    'sign in here',
    '香港 → 美国',
    'HK-JP 专线',
    '🇭🇰 🇺🇸 双线路',
    '192.0.2.1:443',
    '',
  ])('does not invent a single region for %s', (name) => {
    expect(nodeRegion(name)).toBeUndefined()
  })
  it('replaces a leading flag only in presentation and retains every name character', () => {
    const original = '🇭🇰 香港长名称专线'
    expect(nodeRegionDisplayName(original, nodeRegion(original))).toBe('香港长名称专线')
    expect(original).toBe('🇭🇰 香港长名称专线')
    expect(nodeRegionDisplayName('香港01', nodeRegion('香港01'))).toBe('香港01')
    expect(nodeRegionDisplayName('🌐 自动选择', undefined)).toBe('🌐 自动选择')
  })
})
