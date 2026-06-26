import * as THREE from "three";
import { GRAND_ROOT_ID } from "./scene/constants.js";
import { ChannelGraph } from "./scene/channelGraph.js";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import { EffectComposer } from "three/examples/jsm/postprocessing/EffectComposer.js";
import { RenderPass } from "three/examples/jsm/postprocessing/RenderPass.js";
import { UnrealBloomPass } from "three/examples/jsm/postprocessing/UnrealBloomPass.js";

// ループ内で一時オブジェクトを使い回し、毎フレームの GC を減らします。
const scratchObject = new THREE.Object3D();
// 色計算用の一時 Color です。
const scratchColor = new THREE.Color();
// ベクトル計算用の一時 Vector3 です。
const scratchVector = new THREE.Vector3();
// もう一つ同時に必要なときの一時 Vector3 です。
const scratchVectorB = new THREE.Vector3();

// createTraqScene は Vue から呼ばれる Three.js シーンの入口です。
export function createTraqScene(container, callbacks = {}) {
    return new TraqTopologyScene(container, callbacks);
}

class TraqTopologyScene {
    constructor(container, callbacks) {
        // Vue 側から渡された DOM 要素に canvas を追加します。
        this.container = container;
        // onStats/onHover/onContextLost など、Vue へ状態を返す callback 群です。
        this.callbacks = callbacks;
        // ChannelGraph は init イベントを受け取るまで null です。
        this.graph = null;
        // チャンネルノードを描く InstancedMesh です。
        this.mesh = null;
        // 親子リンクを描く InstancedMesh です。
        this.links = null;
        // linkPairs は [parentNode, childNode] の配列で、links の instance と対応します。
        this.linkPairs = [];
        // requestAnimationFrame の ID を保持し、dispose 時に止めます。
        this.frameId = 0;
        // 前フレーム時刻から delta 秒を出すための基準です。
        this.lastFrame = performance.now();
        // onStats を間引くため、最後に通知した時刻を持ちます。
        this.lastStatsAt = 0;
        // raycast を間引くため、最後に実行した時刻を持ちます。
        this.lastRaycastAt = 0;
        // 現在 hover 中のノードです。
        this.hoveredNode = null;
        // タブ非表示時は一部エフェクトを止めるため visibility state を保持します。
        this.hidden = document.visibilityState === "hidden";
        // メッセージ発生後、祖先へ順番に波紋を出す予約キューです。
        this.scheduledRipples = [];
        // dispose が必要な geometry/material をまとめて保持します。
        this.disposables = [];

        // Three.js の描画シーンを作り、背景色と fog を設定します。
        this.scene = new THREE.Scene();
        this.scene.background = new THREE.Color("#030712");
        this.scene.fog = new THREE.FogExp2("#030712", 0.008);

        // 3D 空間を見るカメラを作り、初期位置を少し斜め上に置きます。
        this.camera = new THREE.PerspectiveCamera(52, 1, 0.1, 1200);
        this.camera.position.set(46, 38, 92);

        // WebGL renderer を作り、パフォーマンス優先で antialias を有効にします。
        this.renderer = new THREE.WebGLRenderer({
            antialias: true,
            powerPreference: "high-performance",
        });
        // 高 DPI 端末でも重くなりすぎないよう pixel ratio を 1.5 までに制限します。
        this.renderer.setPixelRatio(
            Math.min(window.devicePixelRatio || 1, 1.5),
        );
        // 色空間と tone mapping を設定し、発光色が自然に見えるようにします。
        this.renderer.outputColorSpace = THREE.SRGBColorSpace;
        this.renderer.toneMapping = THREE.ACESFilmicToneMapping;
        this.renderer.toneMappingExposure = 1.08;
        // CSS で全画面表示するための class を付けます。
        this.renderer.domElement.className = "threeCanvas";
        // Vue の stage 要素へ canvas を差し込みます。
        container.appendChild(this.renderer.domElement);

        // OrbitControls により、ユーザーがドラッグ/ズームで全体を眺められます。
        this.controls = new OrbitControls(
            this.camera,
            this.renderer.domElement,
        );
        // damping を入れて、操作後に少し慣性が残る見た目にします。
        this.controls.enableDamping = true;
        this.controls.dampingFactor = 0.065;
        // 操作速度とズーム範囲を、巨大なチャンネル木でも扱いやすい値にします。
        this.controls.rotateSpeed = 0.45;
        this.controls.zoomSpeed = 0.72;
        this.controls.panSpeed = 0.35;
        this.controls.minDistance = 18;
        this.controls.maxDistance = 520;

        // postprocessing composer を使い、Bloom を重ねて発光表現にします。
        this.composer = new EffectComposer(this.renderer);
        this.renderPass = new RenderPass(this.scene, this.camera);
        this.bloomPass = new UnrealBloomPass(
            new THREE.Vector2(1, 1),
            0.86,
            0.42,
            0.08,
        );
        this.composer.addPass(this.renderPass);
        this.composer.addPass(this.bloomPass);

        // pointer hover 判定用の raycaster と正規化 pointer 座標です。
        this.raycaster = new THREE.Raycaster();
        this.pointer = new THREE.Vector2();

        // 星背景、エフェクト pool、イベント listener、初期サイズ、animation loop を順番に準備します。
        this.createBackdrop();
        this.createEffectPools();
        this.bindEvents();
        this.resize();
        this.animate(performance.now());
    }

    bindEvents() {
        // リサイズ時は canvas と camera aspect を更新します。
        this.handleResize = () => this.resize();
        // pointermove は raycast して hover 中ノードを探します。
        this.handlePointerMove = (event) => this.raycast(event);
        // canvas 外へ出たら hover を解除します。
        this.handlePointerLeave = () => this.setHoveredNode(null);
        // タブ表示状態が変わったら hidden を更新し、復帰時は古い一時エフェクトを消します。
        this.handleVisibility = () => {
            this.hidden = document.visibilityState === "hidden";
            this.lastFrame = performance.now();
            if (!this.hidden) {
                this.clearTransientEffects();
            }
        };
        // WebGL context lost はブラウザ都合で起きるため、Vue 側へ通知します。
        this.handleContextLost = (event) => {
            event.preventDefault();
            this.callbacks.onContextLost?.();
        };

        // window/document/canvas に必要なイベント listener を登録します。
        window.addEventListener("resize", this.handleResize);
        document.addEventListener("visibilitychange", this.handleVisibility);
        this.renderer.domElement.addEventListener(
            "pointermove",
            this.handlePointerMove,
        );
        this.renderer.domElement.addEventListener(
            "pointerleave",
            this.handlePointerLeave,
        );
        this.renderer.domElement.addEventListener(
            "webglcontextlost",
            this.handleContextLost,
            false,
        );
    }

    createBackdrop() {
        // 背景の星を Points でまとめて描き、奥行き感を作ります。
        const starCount = 1100;
        // position/color は BufferAttribute として GPU へ渡します。
        const positions = new Float32Array(starCount * 3);
        const colors = new Float32Array(starCount * 3);
        const starColor = new THREE.Color();
        for (let index = 0; index < starCount; index += 1) {
            // 半径を広めに取り、チャンネル木の周囲を包む球殻に星を置きます。
            const radius = 110 + Math.random() * 360;
            const theta = Math.random() * Math.PI * 2;
            const phi = Math.acos(THREE.MathUtils.randFloatSpread(2));
            // 球面座標を xyz に変換して position 配列へ入れます。
            positions[index * 3] = radius * Math.sin(phi) * Math.cos(theta);
            positions[index * 3 + 1] = radius * Math.cos(phi);
            positions[index * 3 + 2] = radius * Math.sin(phi) * Math.sin(theta);
            // 青寄りの HSL で少しずつ明るさを変えます。
            starColor.setHSL(
                0.58 + Math.random() * 0.12,
                0.38,
                0.42 + Math.random() * 0.34,
            );
            // Color の r/g/b を attribute 用配列へ入れます。
            colors[index * 3] = starColor.r;
            colors[index * 3 + 1] = starColor.g;
            colors[index * 3 + 2] = starColor.b;
        }

        // geometry に position/color attribute を設定します。
        const geometry = new THREE.BufferGeometry();
        geometry.setAttribute(
            "position",
            new THREE.BufferAttribute(positions, 3),
        );
        geometry.setAttribute("color", new THREE.BufferAttribute(colors, 3));
        // AdditiveBlending と透明度で、暗い背景に淡く光る星にします。
        const material = new THREE.PointsMaterial({
            size: 0.62,
            vertexColors: true,
            transparent: true,
            opacity: 0.62,
            depthWrite: false,
            blending: THREE.AdditiveBlending,
        });
        // Points として scene に追加します。
        const stars = new THREE.Points(geometry, material);
        this.scene.add(stars);
        // dispose 時に geometry/material を解放できるよう登録します。
        this.disposables.push(geometry, material);
    }

    createEffectPools() {
        // 波紋は頻繁に出るため、都度 new せず pool から再利用します。
        this.ripplePool = Array.from({ length: 72 }, () => {
            const geometry = new THREE.TorusGeometry(1, 0.018, 6, 64);
            const material = new THREE.MeshBasicMaterial({
                color: "#ffffff",
                transparent: true,
                opacity: 0,
                depthWrite: false,
                blending: THREE.AdditiveBlending,
            });
            const mesh = new THREE.Mesh(geometry, material);
            // 未使用状態では描画しないよう hidden にします。
            mesh.visible = false;
            // userData.effect に bornAt/ttl/node などを入れて、updateEffects で進行します。
            mesh.userData.effect = null;
            this.scene.add(mesh);
            this.disposables.push(geometry, material);
            return mesh;
        });

        // メッセージ発生時に root から target へ飛ぶ粒子の pool です。
        this.particlePool = Array.from({ length: 48 }, () => {
            const geometry = new THREE.SphereGeometry(0.58, 12, 8);
            const material = new THREE.MeshBasicMaterial({
                color: "#ffffff",
                transparent: true,
                opacity: 0,
                depthWrite: false,
                blending: THREE.AdditiveBlending,
            });
            const mesh = new THREE.Mesh(geometry, material);
            mesh.visible = false;
            mesh.userData.effect = null;
            this.scene.add(mesh);
            this.disposables.push(geometry, material);
            return mesh;
        });

        // ユーザー移動時に from から to へ伸びるビームの pool です。
        this.beamPool = Array.from({ length: 72 }, () => {
            const geometry = new THREE.CylinderGeometry(1, 1, 1, 8, 1, true);
            const material = new THREE.MeshBasicMaterial({
                color: "#ffffff",
                transparent: true,
                opacity: 0,
                depthWrite: false,
                blending: THREE.AdditiveBlending,
            });
            const mesh = new THREE.Mesh(geometry, material);
            mesh.visible = false;
            mesh.userData.effect = null;
            this.scene.add(mesh);
            this.disposables.push(geometry, material);
            return mesh;
        });
    }

    setChannels(channels) {
        // init SSE で届いたチャンネルツリーから、配置済みの ChannelGraph を作ります。
        this.graph = new ChannelGraph(channels);
        // graph のノード数に合わせて node/link の InstancedMesh を作り直します。
        this.rebuildNodes();
        this.rebuildLinks();

        // graph.extent に応じてカメラ距離を決め、全体が画面に収まりやすくします。
        const distance = THREE.MathUtils.clamp(
            this.graph.extent * 1.35,
            78,
            360,
        );
        // 少し斜め上から見る位置にカメラを置きます。
        this.camera.position.set(distance * 0.45, distance * 0.36, distance);
        // OrbitControls の注視点を原点へ戻します。
        this.controls.target.set(0, 0, 0);
        this.controls.update();
        // 初回表示のカクつきを抑えるため、シーンを先に compile します。
        this.renderer.compile(this.scene, this.camera);
        // init 直後はすぐ HUD の node 数などを更新します。
        this.emitStats(true);
    }

    rebuildNodes() {
        // 既存 mesh がある場合は scene から外して GPU リソースを解放します。
        if (this.mesh) {
            this.scene.remove(this.mesh);
            this.mesh.geometry.dispose();
            this.mesh.material.dispose();
            this.mesh = null;
        }

        // 各チャンネルノードは同じ sphere geometry を instance として大量描画します。
        const geometry = new THREE.SphereGeometry(1, 18, 12);
        // ShaderMaterial を使い、instanceColor と rim light で発光感を出します。
        const material = new THREE.ShaderMaterial({
            vertexShader: `
        varying vec3 vColor;
        varying vec3 vNormal;

        void main() {
          vColor = instanceColor;
          vNormal = normalize(normalMatrix * mat3(instanceMatrix) * normal);
          gl_Position = projectionMatrix * modelViewMatrix * instanceMatrix * vec4(position, 1.0);
        }
      `,
            fragmentShader: `
        varying vec3 vColor;
        varying vec3 vNormal;

        void main() {
          float rim = pow(1.0 - abs(dot(normalize(vNormal), vec3(0.0, 0.0, 1.0))), 2.0);
          vec3 color = vColor * (0.76 + rim * 0.72);
          gl_FragColor = vec4(color, 1.0);
        }
      `,
            blending: THREE.AdditiveBlending,
            depthWrite: false,
            toneMapped: false,
        });
        this.mesh = new THREE.InstancedMesh(
            geometry,
            material,
            this.graph.nodes.length,
        );
        this.mesh.instanceMatrix.setUsage(THREE.DynamicDrawUsage);
        this.mesh.frustumCulled = false;
        this.scene.add(this.mesh);
        this.disposables.push(geometry, material);
        this.updateNodeInstances(performance.now());
    }

    rebuildLinks() {
        if (this.links) {
            this.scene.remove(this.links);
            this.links.geometry.dispose();
            this.links.material.dispose();
            this.links = null;
        }

        this.linkPairs = [];
        for (const node of this.graph.nodes) {
            const parent = this.graph.get(node.parentId);
            if (parent) this.linkPairs.push([parent, node]);
        }

        const geometry = new THREE.CylinderGeometry(1, 1, 1, 8, 1, true);
        const material = new THREE.MeshBasicMaterial({
            color: "#ffffff",
            transparent: true,
            opacity: 0.68,
            depthWrite: false,
            blending: THREE.AdditiveBlending,
            toneMapped: false,
        });
        this.links = new THREE.InstancedMesh(
            geometry,
            material,
            this.linkPairs.length,
        );
        this.links.instanceMatrix.setUsage(THREE.DynamicDrawUsage);
        this.links.frustumCulled = false;

        this.linkPairs.forEach(([, node], index) => {
            this.links.setColorAt(
                index,
                node.color.clone().multiplyScalar(1.35),
            );
        });
        this.updateLinkInstances();

        if (this.links.instanceColor)
            this.links.instanceColor.needsUpdate = true;
        this.scene.add(this.links);
        this.disposables.push(geometry, material);
    }

    updateLinkInstances() {
        if (!this.links) return;

        this.linkPairs.forEach(([parent, node], index) => {
            const direction = scratchVector
                .copy(node.visualPosition)
                .sub(parent.visualPosition);
            const length = Math.max(0.001, direction.length());
            const radius = node.depth <= 1 ? 0.18 : 0.12;
            direction.normalize();

            scratchObject.position
                .copy(parent.visualPosition)
                .add(node.visualPosition)
                .multiplyScalar(0.5);
            scratchObject.quaternion.setFromUnitVectors(
                scratchVectorB.set(0, 1, 0),
                direction,
            );
            scratchObject.scale.set(radius, length, radius);
            scratchObject.updateMatrix();
            this.links.setMatrixAt(index, scratchObject.matrix);
        });

        this.links.instanceMatrix.needsUpdate = true;
    }

    trigger(payload) {
        if (!this.graph) return;
        const now = performance.now();
        if (payload.type === "msg") {
            const target = this.graph.get(payload.ch);
            if (!target) return;
            this.graph.heatAncestors(target, 56);
            if (this.hidden) return;
            const root = this.graph.root ?? target;
            this.activateParticle(root, target, now, 680, target.color);
            this.scheduleRippleChain(target, now + 680);
            return;
        }

        if (payload.type === "mov") {
            const to = this.graph.get(payload.to);
            if (!to) return;
            const from = this.graph.get(payload.from) ?? this.graph.root ?? to;
            this.graph.heatNode(to, 16);
            if (!this.hidden) {
                this.activateBeam(from, to, now, 820, to.color);
            }
        }
    }

    sync(deltas) {
        this.graph?.sync(deltas);
    }

    scheduleRippleChain(node, startAt) {
        let current = node;
        let dueAt = startAt;
        while (current) {
            this.scheduledRipples.push({ node: current, dueAt });
            if (current.id === GRAND_ROOT_ID) break;
            current = this.graph.get(current.parentId);
            dueAt += 145;
        }
    }

    activateRipple(node, now) {
        const mesh =
            this.ripplePool.find((item) => !item.visible) ?? this.ripplePool[0];
        mesh.visible = true;
        mesh.material.color.copy(node.color);
        mesh.userData.effect = {
            node,
            bornAt: now,
            ttl: 920,
        };
    }

    activateParticle(from, to, now, ttl, color) {
        const mesh =
            this.particlePool.find((item) => !item.visible) ??
            this.particlePool[0];
        mesh.visible = true;
        mesh.material.color.copy(color);
        mesh.userData.effect = {
            from,
            to,
            bornAt: now,
            ttl,
        };
    }

    activateBeam(from, to, now, ttl, color) {
        const line =
            this.beamPool.find((item) => !item.visible) ?? this.beamPool[0];
        line.visible = true;
        line.material.color.copy(color);
        line.userData.effect = {
            from,
            to,
            bornAt: now,
            ttl,
        };
    }

    animate(now) {
        const delta = Math.min(0.05, (now - this.lastFrame) / 1000);
        this.lastFrame = now;

        if (this.graph) {
            this.graph.update(delta, now);
            this.updateNodeInstances(now);
            this.updateLinkInstances();
            this.updateScheduledRipples(now);
            this.updateEffects(now);
            this.emitStats();
        }

        this.controls.update();
        this.scene.rotation.y += delta * 0.012;
        this.composer.render();
        this.frameId = requestAnimationFrame((time) => this.animate(time));
    }

    updateNodeInstances(now) {
        if (!this.mesh || !this.graph) return;
        for (const node of this.graph.nodes) {
            const fever =
                node.currentScore > 70
                    ? 1 + Math.sin(now * 0.012 + node.index) * 0.16
                    : 1;
            const scale =
                (node.baseScale + Math.sqrt(node.currentScore) * 0.22) * fever;
            const brightness = THREE.MathUtils.clamp(
                0.42 + node.currentScore / 54,
                0.36,
                2.7,
            );

            scratchObject.position.copy(node.visualPosition);
            scratchObject.scale.setScalar(scale);
            scratchObject.updateMatrix();
            this.mesh.setMatrixAt(node.index, scratchObject.matrix);

            scratchColor.copy(node.color).multiplyScalar(brightness);
            if (node.id === GRAND_ROOT_ID) {
                scratchColor.setRGB(1.25, 1.38, 1.55);
            }
            this.mesh.setColorAt(node.index, scratchColor);
        }
        this.mesh.instanceMatrix.needsUpdate = true;
        if (this.mesh.instanceColor) this.mesh.instanceColor.needsUpdate = true;
    }

    updateScheduledRipples(now) {
        if (this.hidden || this.scheduledRipples.length === 0) return;
        const pending = [];
        for (const item of this.scheduledRipples) {
            if (item.dueAt <= now) {
                this.activateRipple(item.node, now);
            } else {
                pending.push(item);
            }
        }
        this.scheduledRipples = pending;
    }

    updateEffects(now) {
        for (const mesh of this.ripplePool) {
            const effect = mesh.userData.effect;
            if (!effect) continue;
            const progress = (now - effect.bornAt) / effect.ttl;
            if (progress >= 1) {
                this.releaseEffect(mesh);
                continue;
            }
            mesh.position.copy(effect.node.visualPosition);
            mesh.quaternion.copy(this.camera.quaternion);
            const radius =
                effect.node.baseScale +
                progress * (5.4 + effect.node.depth * 0.35);
            mesh.scale.setScalar(radius);
            mesh.material.opacity = Math.sin(progress * Math.PI) * 0.72;
        }

        for (const mesh of this.particlePool) {
            const effect = mesh.userData.effect;
            if (!effect) continue;
            const progress = (now - effect.bornAt) / effect.ttl;
            if (progress >= 1) {
                this.releaseEffect(mesh);
                continue;
            }
            const eased = easeOutCubic(progress);
            scratchVector
                .copy(effect.from.visualPosition)
                .lerp(effect.to.visualPosition, eased);
            const arc =
                effect.from.visualPosition.distanceTo(
                    effect.to.visualPosition,
                ) * 0.16;
            scratchVector.y += Math.sin(progress * Math.PI) * arc;
            mesh.position.copy(scratchVector);
            const pulse = 1 + Math.sin(progress * Math.PI) * 0.7;
            mesh.scale.setScalar(pulse);
            mesh.material.opacity = Math.sin(progress * Math.PI) * 0.96;
        }

        for (const mesh of this.beamPool) {
            const effect = mesh.userData.effect;
            if (!effect) continue;
            const progress = (now - effect.bornAt) / effect.ttl;
            if (progress >= 1) {
                this.releaseEffect(mesh);
                continue;
            }
            const head = easeOutCubic(progress);
            const tail = Math.max(0, head - 0.24);
            scratchVector
                .copy(effect.from.visualPosition)
                .lerp(effect.to.visualPosition, tail);
            scratchVectorB
                .copy(effect.from.visualPosition)
                .lerp(effect.to.visualPosition, head);
            const direction = scratchVectorB.clone().sub(scratchVector);
            const length = Math.max(0.001, direction.length());
            direction.normalize();
            mesh.position
                .copy(scratchVector)
                .add(scratchVectorB)
                .multiplyScalar(0.5);
            mesh.quaternion.setFromUnitVectors(
                scratchVector.set(0, 1, 0),
                direction,
            );
            mesh.scale.set(0.16, length, 0.16);
            mesh.material.opacity = Math.sin(progress * Math.PI) * 0.82;
        }
    }

    releaseEffect(object) {
        object.visible = false;
        object.userData.effect = null;
        if (object.material) object.material.opacity = 0;
    }

    clearTransientEffects() {
        this.scheduledRipples = [];
        for (const object of [
            ...this.ripplePool,
            ...this.particlePool,
            ...this.beamPool,
        ]) {
            this.releaseEffect(object);
        }
    }

    raycast(event) {
        if (!this.mesh || !this.graph) return;
        const now = performance.now();
        if (now - this.lastRaycastAt < 90) return;
        this.lastRaycastAt = now;

        const rect = this.renderer.domElement.getBoundingClientRect();
        this.pointer.x = ((event.clientX - rect.left) / rect.width) * 2 - 1;
        this.pointer.y = -((event.clientY - rect.top) / rect.height) * 2 + 1;
        this.raycaster.setFromCamera(this.pointer, this.camera);
        const hit = this.raycaster.intersectObject(this.mesh, false)[0];
        const node = hit ? this.graph.nodes[hit.instanceId] : null;
        this.setHoveredNode(node);
    }

    setHoveredNode(node) {
        if (this.hoveredNode?.id === node?.id) return;
        this.hoveredNode = node;
        this.renderer.domElement.style.cursor = node ? "pointer" : "grab";
        this.callbacks.onHover?.(
            node
                ? {
                      id: node.id,
                      name: node.name,
                      score: node.currentScore,
                      color: `#${node.color.getHexString()}`,
                      depth: node.depth,
                  }
                : null,
        );
    }

    emitStats(force = false) {
        const now = performance.now();
        if (!force && now - this.lastStatsAt < 240) return;
        this.lastStatsAt = now;
        this.callbacks.onStats?.({
            nodes: this.graph?.nodes.length ?? 0,
            ripples: this.ripplePool.filter((item) => item.visible).length,
            beams: this.beamPool.filter((item) => item.visible).length,
            activeChannels: this.graph?.topNodes(6) ?? [],
        });
    }

    resize() {
        const width = Math.max(1, this.container.clientWidth);
        const height = Math.max(1, this.container.clientHeight);
        this.camera.aspect = width / height;
        this.camera.updateProjectionMatrix();
        this.renderer.setSize(width, height, false);
        this.composer.setSize(width, height);
        this.bloomPass.setSize(width, height);
    }

    dispose() {
        cancelAnimationFrame(this.frameId);
        window.removeEventListener("resize", this.handleResize);
        document.removeEventListener("visibilitychange", this.handleVisibility);
        this.renderer.domElement.removeEventListener(
            "pointermove",
            this.handlePointerMove,
        );
        this.renderer.domElement.removeEventListener(
            "pointerleave",
            this.handlePointerLeave,
        );
        this.renderer.domElement.removeEventListener(
            "webglcontextlost",
            this.handleContextLost,
        );
        this.controls.dispose();
        for (const disposable of this.disposables) {
            disposable.dispose?.();
        }
        this.composer.dispose();
        this.renderer.dispose();
        this.renderer.domElement.remove();
    }
}

function easeOutCubic(value) {
    return 1 - Math.pow(1 - value, 3);
}
