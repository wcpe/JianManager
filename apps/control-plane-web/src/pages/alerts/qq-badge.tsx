import QRCode from 'react-qr-code'

/**
 * 二维码展示框（FR-494/495）：白底 + 边框统一两处二维码观感（扫码区须为浅底才可识别）。
 * `value` 同时作为 svg 的 `<title>` 与 `aria-label`——DOM 回归断言同值已用于渲染即可，不断言像素。
 */
export function QQQrCodeBox({ value, size = 160, testId }: { value: string; size?: number; testId?: string }) {
  return (
    <div className="flex items-center justify-center rounded-md border bg-white p-3">
      <QRCode value={value} size={size} title={value} aria-label={value} data-testid={testId} />
    </div>
  )
}

/** 扫码绑定二维码展示（FR-494 接入体验改造）：扫一次即取得 AppID / 密钥引用名 / 目标 openid。 */
export function QQBindQrCode({ url, size = 180 }: { url: string; size?: number }) {
  return <QQQrCodeBox value={url} size={size} testId="qq-bind-qrcode" />
}
