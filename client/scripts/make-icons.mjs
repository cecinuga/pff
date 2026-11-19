// Generates the PWA PNG icons (a bar-chart mark on the accent color) without
// any image dependency: pixels are drawn directly and encoded as PNG with zlib.
// Usage: npm run icons
import { writeFileSync, mkdirSync } from "node:fs";
import { deflateSync } from "node:zlib";

const ACCENT = [0x2a, 0x78, 0xd6];
const WHITE = [0xff, 0xff, 0xff];

function crc32(buf) {
  let c = ~0;
  for (const b of buf) {
    c ^= b;
    for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1));
  }
  return ~c >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const td = Buffer.concat([Buffer.from(type), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(td));
  return Buffer.concat([len, td, crc]);
}

function png(size, pixel) {
  const raw = Buffer.alloc((size * 4 + 1) * size);
  for (let y = 0; y < size; y++) {
    raw[y * (size * 4 + 1)] = 0; // filter: none
    for (let x = 0; x < size; x++) {
      const [r, g, b, a] = pixel(x + 0.5, y + 0.5);
      const o = y * (size * 4 + 1) + 1 + x * 4;
      raw[o] = r;
      raw[o + 1] = g;
      raw[o + 2] = b;
      raw[o + 3] = a;
    }
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr[8] = 8; // bit depth
  ihdr[9] = 6; // RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk("IHDR", ihdr),
    chunk("IDAT", deflateSync(raw, { level: 9 })),
    chunk("IEND", Buffer.alloc(0)),
  ]);
}

function inRoundRect(x, y, x0, y0, x1, y1, r) {
  if (x < x0 || x > x1 || y < y0 || y > y1) return false;
  const cx = Math.min(Math.max(x, x0 + r), x1 - r);
  const cy = Math.min(Math.max(y, y0 + r), y1 - r);
  return (x - cx) ** 2 + (y - cy) ** 2 <= r * r;
}

// Geometry in a 64-unit design space (same as icon.svg).
const BARS = [
  [15, 33, 25, 48],
  [29, 23, 39, 48],
  [43, 15, 53, 48],
];

function draw(size, maskable) {
  const s = size / 64;
  // Maskable icons keep content inside the 80% safe zone and fill the canvas.
  const scale = maskable ? 0.72 : 1;
  const off = (64 - 64 * scale) / 2;
  return png(size, (px, py) => {
    const x = px / s;
    const y = py / s;
    const bg = maskable ? true : inRoundRect(x, y, 0, 0, 64, 64, 14);
    if (!bg) return [0, 0, 0, 0];
    const ux = (x - off) / scale;
    const uy = (y - off) / scale;
    const bar = BARS.some(([x0, y0, x1, y1]) => inRoundRect(ux, uy, x0, y0, x1, y1 + 3, 3) && uy <= y1);
    return [...(bar ? WHITE : ACCENT), 255];
  });
}

mkdirSync("public/icons", { recursive: true });
writeFileSync("public/icons/icon-192.png", draw(192, false));
writeFileSync("public/icons/icon-512.png", draw(512, false));
writeFileSync("public/icons/icon-maskable-512.png", draw(512, true));
console.log("icons written to public/icons");
