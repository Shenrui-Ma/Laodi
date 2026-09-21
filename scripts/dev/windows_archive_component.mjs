// Test driver for the hash-verified module emitted by the preparation script.
// All paths, key material and server inputs come from an isolated Go fixture.
import fs from 'node:fs/promises';
import path from 'node:path';
import {pathToFileURL} from 'node:url';

const [modulePath, inputPath, action] = process.argv.slice(2);
const input = JSON.parse(await fs.readFile(inputPath, 'utf8'));
const home = path.resolve(process.env.HOME ?? '');
const inside = p => {
  const relative = path.relative(home, path.resolve(p));
  if (!relative || relative.startsWith('..') || path.isAbsolute(relative)) throw new Error('fixture path escapes synthetic home');
  return p;
};
inside(inputPath); inside(input.workspace); inside(input.result);
const producer = await import(pathToFileURL(modulePath).href);
try {
  if (action === 'create') {
    const scan = await producer.Gme({workspacePath:input.workspace,createdAt:1234567890});
    const extra = await producer.Q3({createdAt:1234567890,inputs:[{groupId:'global-configs',path:'instructions.json',content:'SYNTHETIC_EXTRA_CONFIG',source:'synthetic',changePolicy:'rare'}]});
    const hash = producer.c4(scan.manifest), extraHash = producer.eA(extra.manifest);
    const previous = input.previous ? JSON.parse(await fs.readFile(inside(input.previous),'utf8')) : null;
    const delta = previous ? producer.Pme({baseManifest:previous.manifest,nextManifest:scan.manifest,baseManifestHash:previous.hash,nextManifestHash:hash}) : undefined;
    const files = delta ? scan.files.filter(f=>delta.addedOrModified.some(d=>d.path===f.path)) : scan.files;
    const paths = producer.Eme({workspaceKey:input.workspace,manifestHash:hash,extraManifestHash:extraHash,groupId:input.group});
    Object.values(paths).forEach(inside);
    const result = await producer.Sme({kind:previous?'increment':'baseline',workspaceKeyHash:producer.wa(input.workspace),manifestHash:hash,baseManifestHash:previous?.hash,
      prompt:{schema:'repo_snapshot_prompt/v2',content:'synthetic task'},manifest:scan.manifest,delta,extraManifest:extra.manifest,files,extraFiles:extra.files,paths,
      uploadKey:{snapshotId:'synthetic-snapshot',keyId:'synthetic-key',publicKeySpkiPem:input.publicKey},maxEncryptedArtifactBytes:16*1024*1024});
    await fs.writeFile(input.result,JSON.stringify({paths,hash,manifest:scan.manifest,encryptedSha256:result.encryptedSha256}));
  } else if (action === 'send') {
    const url = new URL(input.receiver);
    if (url.protocol!=='http:' || url.hostname!=='127.0.0.1') throw new Error('receiver must be local');
    const result=JSON.parse(await fs.readFile(input.result,'utf8'));
    const body=await fs.readFile(inside(result.paths.encryptedArtifactPath));
    const envelope=JSON.parse(await fs.readFile(inside(result.paths.envelopePath),'utf8'));
    const response=await fetch(url,{method:'POST',body:JSON.stringify({envelope,ciphertext:body.toString('base64')})});
    if(!response.ok) throw new Error('local receiver rejected fixture');
  } else throw new Error('unknown fixture action');
  console.log(JSON.stringify({ok:true,action}));
} catch (error) {
  console.log(JSON.stringify({ok:false,action,code:error.code??'component_failure',message:error.code?undefined:error.message}));
  process.exitCode=1;
}
