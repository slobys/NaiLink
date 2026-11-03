/** Name hints only: never resolve a server or infer an actual exit IP location. */
const definitions = [
  ['hk', '香港', '香港|香港特別行政區|Hong[ -]?Kong', 'HK|HKG'],
  ['tw', '台湾', '台湾|台灣|台北|Taiwan|Taipei', 'TW|TWN'],
  ['mo', '澳门', '澳门|澳門|Macau|Macao', 'MO|MAC'],
  ['cn', '中国大陆', '中国|中國|大陆|大陸|China|Mainland', 'CN|CHN'],
  ['jp', '日本', '日本|东京|東京|大阪|Japan|Tokyo|Osaka', 'JP|JPN'],
  ['sg', '新加坡', '新加坡|狮城|獅城|Singapore', 'SG|SGP'],
  ['us', '美国', '美国|美國|洛杉矶|洛杉磯|纽约|紐約|圣何塞|United States|Los Angeles|New York', 'US|USA'],
  ['gb', '英国', '英国|英國|伦敦|倫敦|United Kingdom|Britain|London', 'GB|GBR|UK'],
  ['de', '德国', '德国|德國|法兰克福|法蘭克福|Germany|Frankfurt', 'DE|DEU'],
  ['fr', '法国', '法国|法國|巴黎|France|Paris', 'FR|FRA'],
  ['kr', '韩国', '韩国|韓國|首尔|首爾|South Korea|Korea|Seoul', 'KR|KOR'],
  ['ca', '加拿大', '加拿大|多伦多|多倫多|温哥华|溫哥華|Canada|Toronto|Vancouver', 'CA|CAN'],
  ['au', '澳大利亚', '澳大利亚|澳大利亞|澳洲|悉尼|雪梨|Australia|Sydney', 'AU|AUS'],
  ['nz', '新西兰', '新西兰|新西蘭|纽西兰|紐西蘭|New Zealand', 'NZ|NZL'],
  ['ru', '俄罗斯', '俄罗斯|俄羅斯|莫斯科|Russia|Moscow', 'RU|RUS'],
  ['nl', '荷兰', '荷兰|荷蘭|阿姆斯特丹|Netherlands|Amsterdam', 'NL|NLD'],
  ['ch', '瑞士', '瑞士|苏黎世|蘇黎世|Switzerland|Zurich', 'CH|CHE'],
  ['se', '瑞典', '瑞典|Sweden', 'SE|SWE'],
  ['no', '挪威', '挪威|Norway', 'NO|NOR'],
  ['fi', '芬兰', '芬兰|芬蘭|Finland', 'FI|FIN'],
  ['dk', '丹麦', '丹麦|丹麥|Denmark', 'DK|DNK'],
  ['es', '西班牙', '西班牙|Spain', 'ES|ESP'],
  ['it', '意大利', '意大利|義大利|Italy', 'IT|ITA'],
  ['ie', '爱尔兰', '爱尔兰|愛爾蘭|Ireland', 'IE|IRL'],
  ['pl', '波兰', '波兰|波蘭|Poland', 'PL|POL'],
  ['tr', '土耳其', '土耳其|Turkey|Türkiye', 'TR|TUR'],
  ['ae', '阿联酋', '阿联酋|阿聯酋|迪拜|阿拉伯联合酋长国|United Arab Emirates|Dubai', 'AE|ARE|UAE'],
  ['my', '马来西亚', '马来西亚|馬來西亞|马来|馬來|大马|Malaysia', 'MY|MYS'],
  ['th', '泰国', '泰国|泰國|曼谷|Thailand|Bangkok', 'TH|THA'],
  ['vn', '越南', '越南|Vietnam', 'VN|VNM'],
  ['ph', '菲律宾', '菲律宾|菲律賓|Philippines', 'PH|PHL'],
  ['id', '印度尼西亚', '印度尼西亚|印度尼西亞|印尼|Indonesia', 'ID|IDN'],
  ['in', '印度', '印度(?!尼)|India', 'IN|IND'],
  ['za', '南非', '南非|South Africa', 'ZA|ZAF'],
  ['br', '巴西', '巴西|Brazil', 'BR|BRA'],
  ['mx', '墨西哥', '墨西哥|Mexico', 'MX|MEX'],
  ['ar', '阿根廷', '阿根廷|Argentina', 'AR|ARG'],
  ['ua', '乌克兰', '乌克兰|烏克蘭|Ukraine', 'UA|UKR'],
  ['il', '以色列', '以色列|Israel', 'IL|ISR'],
  ['is', '冰岛', '冰岛|冰島|Iceland', 'IS|ISL'],
  ['kz', '哈萨克斯坦', '哈萨克斯坦|哈薩克斯坦|Kazakhstan', 'KZ|KAZ'],
  ['sa', '沙特阿拉伯', '沙特|Saudi Arabia', 'SA|SAU'],
] as const

export type NodeRegionCode = (typeof definitions)[number][0]
export interface NodeRegion {
  code: NodeRegionCode
  name: string
}

const regions = definitions.map(([code, name, aliases, abbreviations]) => ({
  code,
  name,
  // English words must not be fragments of another word; Chinese names may
  // directly precede a number/provider. Code tokens may precede node numbers.
  label: new RegExp(
    `(?:${aliases
      .split('|')
      .map((alias) => (/[a-z]/i.test(alias) ? `(?:^|[^a-z])(?:${alias})(?![a-z])` : alias))
      .join('|')})`,
    'i',
  ),
  abbreviation: new RegExp(`(?:^|[^a-z0-9])(${abbreviations})(?=$|[^a-z])`, 'gi'),
}))
const byCode = new Map<string, NodeRegion>(regions.map(({ code, name }) => [code, { code, name }]))
const flagPattern = /[\u{1F1E6}-\u{1F1FF}]{2}/gu

export function nodeRegion(name: string): NodeRegion | undefined {
  const normalized = name.normalize('NFKC')
  const flags = normalized.match(flagPattern)
  if (flags?.length) {
    const codes = new Set(
      flags.map((flag) =>
        Array.from(flag, (part) => String.fromCharCode(part.codePointAt(0)! - 0x1F1E6 + 97)).join(''),
      ),
    )
    return codes.size === 1 ? byCode.get([...codes][0]) : undefined
  }
  // Domain labels such as hk.example.com are not a declared node region.
  const labels = normalized
    .replace(/[a-z][a-z0-9+.-]*:\/\/\S+/gi, ' ')
    .replace(/\b(?:[a-z0-9-]+\.)+[a-z]{2,}(?::\d+)?(?:\/\S*)?/gi, ' ')
  const matches = regions.filter(({ label, abbreviation }) => {
    if (label.test(labels)) return true
    abbreviation.lastIndex = 0
    // Lowercase English prepositions/words are not region hints on their own.
    for (const match of labels.matchAll(abbreviation)) {
      if (
        !['in', 'it', 'no', 'is', 'id'].includes(match[1]) ||
        /\d/.test(labels[(match.index || 0) + match[0].length] || '') ||
        labels.trim() === match[1]
      )
        return true
    }
    return false
  })
  // Multi-region chain names do not identify a single exit. Do not guess.
  return matches.length === 1 ? byCode.get(matches[0].code) : undefined
}

export function nodeRegionDisplayName(name: string, region?: NodeRegion) {
  // Replace an existing leading emoji flag with the bundled SVG, preserving
  // the original stored/searchable name and accessible button labels.
  return region ? name.replace(/^(?:\s*[\u{1F1E6}-\u{1F1FF}]{2})+\s*/u, '') || name : name
}
