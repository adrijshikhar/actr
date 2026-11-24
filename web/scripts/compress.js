import fs from 'fs';
import path from 'path';
import zlib from 'zlib';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);
const distDir = path.resolve(__dirname, '../../internal/web/dist');

function compressDir(dir) {
  if (!fs.existsSync(dir)) return;
  const entries = fs.readdirSync(dir, { withFileTypes: true });
  for (const entry of entries) {
    const fullPath = path.join(dir, entry.name);
    if (entry.isDirectory()) {
      compressDir(fullPath);
    } else if (entry.isFile()) {
      if (entry.name.endsWith('.gz')) continue;
      const content = fs.readFileSync(fullPath);
      const gzipped = zlib.gzipSync(content, { level: 9 });
      fs.writeFileSync(`${fullPath}.gz`, gzipped);
      fs.unlinkSync(fullPath);
    }
  }
}

compressDir(distDir);
console.log('Static assets in internal/web/dist compressed to .gz');
