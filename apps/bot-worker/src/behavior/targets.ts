export interface BehaviorPoint {
  x: number
  y: number
  z: number
}

/** 解析 `x,y,z;x,y,z` 格式的行为目标点。 */
export function parseBehaviorTargetPoints(target: string): BehaviorPoint[] {
  return target
    .split(';')
    .map((part) => part.trim())
    .filter((part) => part.length > 0)
    .map(parseBehaviorPoint)
    .filter((point): point is BehaviorPoint => point !== null)
}

function parseBehaviorPoint(value: string): BehaviorPoint | null {
  const parts = value.split(',').map((item) => Number(item.trim()))
  if (parts.length !== 3 || parts.some((item) => !Number.isFinite(item))) {
    return null
  }
  return { x: parts[0], y: parts[1], z: parts[2] }
}
