import * as THREE from "three";
import { OrbitControls } from "three/examples/jsm/controls/OrbitControls.js";
import { EffectComposer } from "three/examples/jsm/postprocessing/EffectComposer.js";
import { RenderPass } from "three/examples/jsm/postprocessing/RenderPass.js";
import { UnrealBloomPass } from "three/examples/jsm/postprocessing/UnrealBloomPass.js";

const GRAND_ROOT_ID = "grand_root";
const GOLDEN_ANGLE = Math.PI * (3 - Math.sqrt(5));
const PALETTE = [
    "#ff6b6b",
    "#ffd166",
    "#06d6a0",
    "#4cc9f0",
    "#b5179e",
    "#f8961e",
    "#90be6d",
    "#577590",
    "#f72585",
];
const ROOT_CLUSTER_PADDING = 18;
const ROOT_MIN_DISTANCE = 54;
const ROOT_DIRECTION_JITTER = 0.16;
const BRANCH_JITTER_MIN = 0.9;
const BRANCH_JITTER_MAX = 1.1;
const DRIFT_BASE_AMPLITUDE = 0.18;
const DRIFT_DEPTH_AMPLITUDE = 0.045;

const scratchObject = new THREE.Object3D();
const scratchColor = new THREE.Color();
const scratchVector = new THREE.Vector3();
const scratchVectorB = new THREE.Vector3();
const scratchVectorC = new THREE.Vector3();
const defaultZ = new THREE.Vector3(0, 0, 1);

export function createTraqScene(container, callbacks = {}) {
    return new TraqTopologyScene(container, callbacks);
}

class ChannelGraph {
    constructor(channels) {
        this.nodes = [];
        this.nodeMap = new Map();
        this.extent = 50;
        this.root = null;

        const source = new Map(
            Object.values(channels).map((channel) => [channel.id, channel]),
        );
        const ordered = [];
        const seen = new Set();

        const visit = (id) => {
            const channel = source.get(id);
            if (!channel || seen.has(id)) return;
            seen.add(id);
            ordered.push(channel);
            for (const childId of channel.children ?? []) {
                visit(childId);
            }
        };

        visit(GRAND_ROOT_ID);
        for (const channel of source.values()) {
            visit(channel.id);
        }

        for (const channel of ordered) {
            const node = {
                index: this.nodes.length,
                id: channel.id,
                name: channel.name,
                parentId: channel.parentId || "",
                childIds: channel.children ?? [],
                children: [],
                islandId: channel.islandId ?? -1,
                depth: channel.depth ?? 0,
                currentScore: 0,
                targetScore: 0,
                position: new THREE.Vector3(),
                visualPosition: new THREE.Vector3(),
                color: colorForChannel(channel),
                baseScale:
                    channel.id === GRAND_ROOT_ID
                        ? 1.7
                        : Math.max(0.42, 1.35 - (channel.depth ?? 0) * 0.16),
                layoutRadius: 0,
                driftPhase: Math.random() * Math.PI * 2,
                driftSpeed: 0.34 + Math.random() * 0.22,
                driftAmplitude:
                    channel.id === GRAND_ROOT_ID
                        ? 0
                        : DRIFT_BASE_AMPLITUDE +
                          Math.min(channel.depth ?? 0, 8) *
                              DRIFT_DEPTH_AMPLITUDE,
                driftAxisA: randomUnitVector(),
                driftAxisB: randomUnitVector(),
            };
            node.driftAxisB
                .crossVectors(node.driftAxisA, node.driftAxisB)
                .normalize();
            if (node.driftAxisB.lengthSq() < 0.001) {
                node.driftAxisB
                    .crossVectors(node.driftAxisA, defaultZ)
                    .normalize();
            }
            this.nodes.push(node);
            this.nodeMap.set(node.id, node);
            if (node.id === GRAND_ROOT_ID) this.root = node;
        }

        for (const node of this.nodes) {
            node.children = node.childIds
                .map((id) => this.nodeMap.get(id))
                .filter(Boolean);
        }

        if (!this.root && this.nodes.length > 0) {
            this.root = this.nodes[0];
        }
        if (this.root) {
            this.computeLayoutRadii();
            this.placeNodes();
            this.updateVisualPositions(performance.now());
        }
    }

    computeLayoutRadii() {
        const compute = (node) => {
            if (node.children.length === 0) {
                node.layoutRadius = node.baseScale;
                return node.layoutRadius;
            }

            const childDistance =
                branchDistanceForDepth(node.depth) * BRANCH_JITTER_MAX;
            node.layoutRadius = Math.max(
                node.baseScale,
                ...node.children.map(
                    (child) => childDistance + compute(child) + child.baseScale,
                ),
            );
            return node.layoutRadius;
        };

        compute(this.root);
    }

    placeNodes() {
        const rootDirections =
            this.root.children.length > 0
                ? rotatedFibonacciDirections(
                      this.root.children.length,
                      ROOT_DIRECTION_JITTER,
                  )
                : [];
        const rootDistance =
            this.root.children.length > 0
                ? this.rootDistanceFor(rootDirections)
                : ROOT_MIN_DISTANCE;

        const place = (node, position, outwardDir) => {
            node.position.copy(position);
            node.visualPosition.copy(position);
            this.extent = Math.max(this.extent, position.length());
            if (node.children.length === 0) return;

            const childCount = node.children.length;
            const baseDistance = branchDistanceForDepth(node.depth);
            const maxSpreadAngle = spreadAngleForDepth(node.depth);
            const offset = Math.random() * Math.PI * 2;
            const quaternion = new THREE.Quaternion().setFromUnitVectors(
                defaultZ,
                outwardDir.clone().normalize(),
            );

            for (let index = 0; index < childCount; index += 1) {
                const child = node.children[index];
                let direction;

                if (node.id === GRAND_ROOT_ID && rootDirections[index]) {
                    direction = rootDirections[index].clone();
                } else {
                    const spread =
                        childCount === 1 ? 0 : index / (childCount - 1);
                    const z = THREE.MathUtils.clamp(
                        1 - spread * (1 - Math.cos(maxSpreadAngle)),
                        -1,
                        1,
                    );
                    const radius = Math.sqrt(Math.max(0, 1 - z * z));
                    const theta = index * GOLDEN_ANGLE + offset;
                    direction = new THREE.Vector3(
                        Math.cos(theta) * radius,
                        Math.sin(theta) * radius,
                        z,
                    );
                    direction.applyQuaternion(quaternion).normalize();
                }

                const jitter =
                    node.id === GRAND_ROOT_ID
                        ? 1
                        : BRANCH_JITTER_MIN +
                          Math.random() *
                              (BRANCH_JITTER_MAX - BRANCH_JITTER_MIN);
                const childPosition = position
                    .clone()
                    .add(direction.multiplyScalar(baseDistance * jitter));
                place(
                    child,
                    childPosition,
                    childPosition.clone().sub(position).normalize(),
                );
            }
        };

        place(
            this.root,
            new THREE.Vector3(0, 0, 0),
            new THREE.Vector3(0, 1, 0),
        );
    }

    rootDistanceFor(directions) {
        let requiredDistance = ROOT_MIN_DISTANCE;

        for (let i = 0; i < this.root.children.length; i += 1) {
            for (let j = i + 1; j < this.root.children.length; j += 1) {
                const chord = Math.max(
                    0.08,
                    directions[i].distanceTo(directions[j]),
                );
                const required =
                    (this.root.children[i].layoutRadius +
                        this.root.children[j].layoutRadius +
                        ROOT_CLUSTER_PADDING) /
                    chord;
                requiredDistance = Math.max(requiredDistance, required);
            }
        }

        return requiredDistance;
    }

    get(id) {
        return this.nodeMap.get(id);
    }

    heatAncestors(node, amount) {
        let current = node;
        let heat = amount;
        while (current) {
            current.currentScore = Math.min(100, current.currentScore + heat);
            current.targetScore = Math.max(
                current.targetScore,
                current.currentScore * 0.62,
            );
            if (current.id === GRAND_ROOT_ID) break;
            current = this.nodeMap.get(current.parentId);
            heat *= 0.45;
        }
    }

    heatNode(node, amount) {
        node.currentScore = Math.min(100, node.currentScore + amount);
        node.targetScore = Math.max(node.targetScore, node.currentScore * 0.7);
    }

    sync(deltas) {
        for (const [id, score] of Object.entries(deltas ?? {})) {
            const node = this.nodeMap.get(id);
            if (node) {
                node.targetScore = score;
            }
        }
    }

    update(delta, now) {
        const decay = Math.exp(-delta * 0.78);
        const targetDecay = Math.exp(-delta * 0.48);
        for (const node of this.nodes) {
            node.currentScore *= decay;
            node.targetScore *= targetDecay;
            node.currentScore += (node.targetScore - node.currentScore) * 0.07;
            if (node.currentScore < 0.015) node.currentScore = 0;
        }
        this.updateVisualPositions(now);
    }

    updateVisualPositions(now) {
        const time = now * 0.001;
        for (const node of this.nodes) {
            const heatLift = THREE.MathUtils.clamp(
                node.currentScore / 160,
                0,
                0.7,
            );
            const amplitude = node.driftAmplitude * (1 + heatLift);
            const waveA =
                Math.sin(time * node.driftSpeed + node.driftPhase) * amplitude;
            const waveB =
                Math.cos(
                    time * node.driftSpeed * 0.73 + node.driftPhase * 1.31,
                ) *
                amplitude *
                0.58;
            node.visualPosition
                .copy(node.position)
                .addScaledVector(node.driftAxisA, waveA)
                .addScaledVector(node.driftAxisB, waveB);
        }
    }

    topNodes(limit) {
        return this.nodes
            .filter((node) => node.id !== GRAND_ROOT_ID)
            .sort((a, b) => b.currentScore - a.currentScore)
            .slice(0, limit)
            .map((node) => ({
                id: node.id,
                name: node.name,
                score: node.currentScore,
                color: `#${node.color.getHexString()}`,
            }));
    }
}

class TraqTopologyScene {
    constructor(container, callbacks) {
        this.container = container;
        this.callbacks = callbacks;
        this.graph = null;
        this.mesh = null;
        this.links = null;
        this.linkPairs = [];
        this.frameId = 0;
        this.lastFrame = performance.now();
        this.lastStatsAt = 0;
        this.lastRaycastAt = 0;
        this.hoveredNode = null;
        this.hidden = document.visibilityState === "hidden";
        this.scheduledRipples = [];
        this.disposables = [];

        this.scene = new THREE.Scene();
        this.scene.background = new THREE.Color("#030712");
        this.scene.fog = new THREE.FogExp2("#030712", 0.008);

        this.camera = new THREE.PerspectiveCamera(52, 1, 0.1, 1200);
        this.camera.position.set(46, 38, 92);

        this.renderer = new THREE.WebGLRenderer({
            antialias: true,
            powerPreference: "high-performance",
        });
        this.renderer.setPixelRatio(
            Math.min(window.devicePixelRatio || 1, 1.5),
        );
        this.renderer.outputColorSpace = THREE.SRGBColorSpace;
        this.renderer.toneMapping = THREE.ACESFilmicToneMapping;
        this.renderer.toneMappingExposure = 1.08;
        this.renderer.domElement.className = "threeCanvas";
        container.appendChild(this.renderer.domElement);

        this.controls = new OrbitControls(
            this.camera,
            this.renderer.domElement,
        );
        this.controls.enableDamping = true;
        this.controls.dampingFactor = 0.065;
        this.controls.rotateSpeed = 0.45;
        this.controls.zoomSpeed = 0.72;
        this.controls.panSpeed = 0.35;
        this.controls.minDistance = 18;
        this.controls.maxDistance = 520;

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

        this.raycaster = new THREE.Raycaster();
        this.pointer = new THREE.Vector2();

        this.createBackdrop();
        this.createEffectPools();
        this.bindEvents();
        this.resize();
        this.animate(performance.now());
    }

    bindEvents() {
        this.handleResize = () => this.resize();
        this.handlePointerMove = (event) => this.raycast(event);
        this.handlePointerLeave = () => this.setHoveredNode(null);
        this.handleVisibility = () => {
            this.hidden = document.visibilityState === "hidden";
            this.lastFrame = performance.now();
            if (!this.hidden) {
                this.clearTransientEffects();
            }
        };
        this.handleContextLost = (event) => {
            event.preventDefault();
            this.callbacks.onContextLost?.();
        };

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
        const starCount = 1100;
        const positions = new Float32Array(starCount * 3);
        const colors = new Float32Array(starCount * 3);
        const starColor = new THREE.Color();
        for (let index = 0; index < starCount; index += 1) {
            const radius = 110 + Math.random() * 360;
            const theta = Math.random() * Math.PI * 2;
            const phi = Math.acos(THREE.MathUtils.randFloatSpread(2));
            positions[index * 3] = radius * Math.sin(phi) * Math.cos(theta);
            positions[index * 3 + 1] = radius * Math.cos(phi);
            positions[index * 3 + 2] = radius * Math.sin(phi) * Math.sin(theta);
            starColor.setHSL(
                0.58 + Math.random() * 0.12,
                0.38,
                0.42 + Math.random() * 0.34,
            );
            colors[index * 3] = starColor.r;
            colors[index * 3 + 1] = starColor.g;
            colors[index * 3 + 2] = starColor.b;
        }

        const geometry = new THREE.BufferGeometry();
        geometry.setAttribute(
            "position",
            new THREE.BufferAttribute(positions, 3),
        );
        geometry.setAttribute("color", new THREE.BufferAttribute(colors, 3));
        const material = new THREE.PointsMaterial({
            size: 0.62,
            vertexColors: true,
            transparent: true,
            opacity: 0.62,
            depthWrite: false,
            blending: THREE.AdditiveBlending,
        });
        const stars = new THREE.Points(geometry, material);
        this.scene.add(stars);
        this.disposables.push(geometry, material);
    }

    createEffectPools() {
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
            mesh.visible = false;
            mesh.userData.effect = null;
            this.scene.add(mesh);
            this.disposables.push(geometry, material);
            return mesh;
        });

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
        this.graph = new ChannelGraph(channels);
        this.rebuildNodes();
        this.rebuildLinks();

        const distance = THREE.MathUtils.clamp(
            this.graph.extent * 1.35,
            78,
            360,
        );
        this.camera.position.set(distance * 0.45, distance * 0.36, distance);
        this.controls.target.set(0, 0, 0);
        this.controls.update();
        this.renderer.compile(this.scene, this.camera);
        this.emitStats(true);
    }

    rebuildNodes() {
        if (this.mesh) {
            this.scene.remove(this.mesh);
            this.mesh.geometry.dispose();
            this.mesh.material.dispose();
            this.mesh = null;
        }

        const geometry = new THREE.SphereGeometry(1, 18, 12);
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

function colorForChannel(channel) {
    if (channel.id === GRAND_ROOT_ID) {
        return new THREE.Color("#d6dde8");
    }
    const color = new THREE.Color(
        PALETTE[Math.max(0, channel.islandId ?? 0) % PALETTE.length],
    );
    const depthFade = THREE.MathUtils.clamp(
        1 - ((channel.depth ?? 0) - 1) * 0.055,
        0.58,
        1,
    );
    return color.lerp(new THREE.Color("#dbe8ff"), 1 - depthFade);
}

function branchDistanceForDepth(depth) {
    return 80 * Math.pow(0.3, depth - 1);
}

function spreadAngleForDepth(depth) {
    if (depth === 0) return Math.PI * 0.96;
    return Math.PI * 0.76 * Math.pow(0.82, depth - 1);
}

function randomUnitVector() {
    const z = THREE.MathUtils.randFloatSpread(2);
    const radius = Math.sqrt(Math.max(0, 1 - z * z));
    const theta = Math.random() * Math.PI * 2;
    return new THREE.Vector3(
        Math.cos(theta) * radius,
        Math.sin(theta) * radius,
        z,
    );
}

function rotatedFibonacciDirections(count, jitter = 0) {
    if (count <= 0) return [];

    const directions = [];
    const rotation = new THREE.Quaternion().setFromEuler(
        new THREE.Euler(
            Math.random() * Math.PI,
            Math.random() * Math.PI,
            Math.random() * Math.PI,
        ),
    );

    for (let index = 0; index < count; index += 1) {
        const y = 1 - ((index + 0.5) / count) * 2;
        const radius = Math.sqrt(Math.max(0, 1 - y * y));
        const theta = index * GOLDEN_ANGLE;
        const direction = new THREE.Vector3(
            Math.cos(theta) * radius,
            y,
            Math.sin(theta) * radius,
        );

        if (jitter > 0) {
            direction
                .addScaledVector(randomUnitVector(), Math.random() * jitter)
                .normalize();
        }

        direction.applyQuaternion(rotation).normalize();
        directions.push(direction);
    }

    return directions;
}

function easeOutCubic(value) {
    return 1 - Math.pow(1 - value, 3);
}
