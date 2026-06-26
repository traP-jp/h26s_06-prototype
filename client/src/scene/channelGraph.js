import * as THREE from "three";
import {
  BRANCH_JITTER_MAX,
  BRANCH_JITTER_MIN,
  DRIFT_BASE_AMPLITUDE,
  DRIFT_DEPTH_AMPLITUDE,
  GOLDEN_ANGLE,
  GRAND_ROOT_ID,
  PALETTE,
  ROOT_CLUSTER_PADDING,
  ROOT_DIRECTION_JITTER,
  ROOT_MIN_DISTANCE,
} from "./constants.js";

const defaultZ = new THREE.Vector3(0, 0, 1);

// ChannelGraph は API から届いたチャンネル木を Three.js のノード配置へ変換します。
export class ChannelGraph {
    constructor(channels) {
        // nodes は描画順と InstancedMesh の instanceId に対応する配列です。
        this.nodes = [];
        // nodeMap は trigger で届く channelId から即座にノードを引くための Map です。
        this.nodeMap = new Map();
        // extent は全ノードが原点からどれくらい広がったかを記録し、カメラ距離に使います。
        this.extent = 50;
        // root は grand_root、なければ最初のノードを入れます。
        this.root = null;

        // API payload は object なので、id から channel を引ける Map に変換します。
        const source = new Map(
            Object.values(channels).map((channel) => [channel.id, channel]),
        );
        // ordered は親が子より先に来るよう並べ直した channel 配列です。
        const ordered = [];
        // seen は循環や重複 children による二重登録を防ぐために使います。
        const seen = new Set();

        // visit は channel tree を深さ優先で辿り、親子順を保って ordered に積みます。
        const visit = (id) => {
            const channel = source.get(id);
            if (!channel || seen.has(id)) return;
            // ここで seen に入れることで、同じ ID が複数親に出ても一度だけ処理します。
            seen.add(id);
            ordered.push(channel);
            // children がない古い/壊れた payload でも落ちないよう nullish coalescing します。
            for (const childId of channel.children ?? []) {
                visit(childId);
            }
        };

        // まず grand_root から辿り、自然な木構造の順序を作ります。
        visit(GRAND_ROOT_ID);
        // grand_root から到達できない孤立ノードも描画対象から落とさないよう最後に拾います。
        for (const channel of source.values()) {
            visit(channel.id);
        }

        // API の channel オブジェクトを、描画とアニメーションに必要な node オブジェクトへ変換します。
        for (const channel of ordered) {
            const node = {
                // index は InstancedMesh の setMatrixAt / setColorAt に使います。
                index: this.nodes.length,
                // id/name は trigger lookup と HUD 表示に使います。
                id: channel.id,
                name: channel.name,
                // parentId/childIds はリンク描画と祖先加熱に使います。
                parentId: channel.parentId || "",
                childIds: channel.children ?? [],
                children: [],
                // islandId は色分け、depth はサイズ・距離・波紋半径の計算に使います。
                islandId: channel.islandId ?? -1,
                depth: channel.depth ?? 0,
                // currentScore は現在の見た目、targetScore は sync で向かう先の熱量です。
                currentScore: 0,
                targetScore: 0,
                // position は静的レイアウト位置、visualPosition は揺れを加えた描画位置です。
                position: new THREE.Vector3(),
                visualPosition: new THREE.Vector3(),
                // 色は island と depth から決め、描画時に明るさだけ変えます。
                color: colorForChannel(channel),
                // root は大きく、深いチャンネルほど小さくして階層感を出します。
                baseScale:
                    channel.id === GRAND_ROOT_ID
                        ? 1.7
                        : Math.max(0.42, 1.35 - (channel.depth ?? 0) * 0.16),
                // layoutRadius は子孫を含めた占有半径で、島同士の衝突回避に使います。
                layoutRadius: 0,
                // drift* はノードを少し漂わせるためのランダム位相・速度・振幅です。
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
            // driftAxisB を driftAxisA と直交させ、2 軸の揺れが同じ方向に潰れないようにします。
            node.driftAxisB
                .crossVectors(node.driftAxisA, node.driftAxisB)
                .normalize();
            if (node.driftAxisB.lengthSq() < 0.001) {
                // ほぼゼロベクトルになった場合は Z 軸との外積で予備の直交軸を作ります。
                node.driftAxisB
                    .crossVectors(node.driftAxisA, defaultZ)
                    .normalize();
            }
            // 配列と Map の両方へ登録し、描画順参照と ID 参照の両方を可能にします。
            this.nodes.push(node);
            this.nodeMap.set(node.id, node);
            // grand_root を見つけたら root として保持します。
            if (node.id === GRAND_ROOT_ID) this.root = node;
        }

        // childIds を実際の node 参照へ張り替え、再帰処理で使いやすくします。
        for (const node of this.nodes) {
            node.children = node.childIds
                .map((id) => this.nodeMap.get(id))
                .filter(Boolean);
        }

        // payload に grand_root がない場合でも、最初のノードを root として最低限描画できるようにします。
        if (!this.root && this.nodes.length > 0) {
            this.root = this.nodes[0];
        }
        // root がある場合だけ、占有半径計算・配置・初期 visualPosition 更新を行います。
        if (this.root) {
            this.computeLayoutRadii();
            this.placeNodes();
            this.updateVisualPositions(performance.now());
        }
    }

    computeLayoutRadii() {
        // compute は子孫を含めたノードの占有半径を再帰的に返します。
        const compute = (node) => {
            if (node.children.length === 0) {
                // leaf は自分自身の baseScale を占有半径とします。
                node.layoutRadius = node.baseScale;
                return node.layoutRadius;
            }

            // 子の配置距離を見込み、子孫の占有半径まで含めて親の半径を決めます。
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

        // root から計算すれば、全子孫の layoutRadius が埋まります。
        compute(this.root);
    }

    placeNodes() {
        // root 直下の島は球面上の Fibonacci 配置で大きく散らします。
        const rootDirections =
            this.root.children.length > 0
                ? rotatedFibonacciDirections(
                      this.root.children.length,
                      ROOT_DIRECTION_JITTER,
                  )
                : [];
        // 島同士が重ならないよう、必要な root からの距離を計算します。
        const rootDistance =
            this.root.children.length > 0
                ? this.rootDistanceFor(rootDirections)
                : ROOT_MIN_DISTANCE;

        // place は node とその子孫を、親から外向き方向へ再帰的に配置します。
        const place = (node, position, outwardDir) => {
            // 静的な基準位置と初期描画位置を同じ座標にします。
            node.position.copy(position);
            node.visualPosition.copy(position);
            // カメラ距離計算用に、最も遠いノード距離を記録します。
            this.extent = Math.max(this.extent, position.length());
            // leaf なら子を配置する必要がないので終了します。
            if (node.children.length === 0) return;

            // 子の数と階層に応じて、枝の距離と広がり角を決めます。
            const childCount = node.children.length;
            const baseDistance = branchDistanceForDepth(node.depth);
            const maxSpreadAngle = spreadAngleForDepth(node.depth);
            // offset で兄弟の角度を少し回し、毎回同じ絵になりすぎないようにします。
            const offset = Math.random() * Math.PI * 2;
            // defaultZ ベースの方向を、親から見た outwardDir へ回転させるクォータニオンです。
            const quaternion = new THREE.Quaternion().setFromUnitVectors(
                defaultZ,
                outwardDir.clone().normalize(),
            );

            for (let index = 0; index < childCount; index += 1) {
                // 今回配置する子ノードです。
                const child = node.children[index];
                let direction;

                if (node.id === GRAND_ROOT_ID && rootDirections[index]) {
                    // grand_root 直下は島配置用に事前計算した方向をそのまま使います。
                    direction = rootDirections[index].clone();
                } else {
                    // それ以外は親の outwardDir 周辺に、子を扇形に広げます。
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

                // root 直下以外は距離に少し揺らぎを入れ、人工的な等間隔感を減らします。
                const jitter =
                    node.id === GRAND_ROOT_ID
                        ? 1
                        : BRANCH_JITTER_MIN +
                          Math.random() *
                              (BRANCH_JITTER_MAX - BRANCH_JITTER_MIN);
                // 親位置から direction * distance だけ進めた場所を子の位置にします。
                const childPosition = position
                    .clone()
                    .add(direction.multiplyScalar(baseDistance * jitter));
                // 子のさらに子は、親から子へ向かう方向を outwardDir として再帰配置します。
                place(
                    child,
                    childPosition,
                    childPosition.clone().sub(position).normalize(),
                );
            }
        };

        // root を原点に置き、最初の outwardDir は上向きとして配置を開始します。
        place(
            this.root,
            new THREE.Vector3(0, 0, 0),
            new THREE.Vector3(0, 1, 0),
        );
    }

    rootDistanceFor(directions) {
        // 最低距離を基準に、島同士の占有半径が重ならない距離まで広げます。
        let requiredDistance = ROOT_MIN_DISTANCE;

        // root 直下の全ペアを見て、必要な chord 距離を満たす rootDistance を求めます。
        for (let i = 0; i < this.root.children.length; i += 1) {
            for (let j = i + 1; j < this.root.children.length; j += 1) {
                // 方向ベクトル同士の距離が小さすぎると割り算が不安定なので下限を置きます。
                const chord = Math.max(
                    0.08,
                    directions[i].distanceTo(directions[j]),
                );
                // 2 島の占有半径と余白を chord で割ると、必要な原点からの距離になります。
                const required =
                    (this.root.children[i].layoutRadius +
                        this.root.children[j].layoutRadius +
                        ROOT_CLUSTER_PADDING) /
                    chord;
                // 全ペアの中で最大の必要距離を採用します。
                requiredDistance = Math.max(requiredDistance, required);
            }
        }

        // placeNodes はこの距離を使って島を原点から離します。
        return requiredDistance;
    }

    get(id) {
        // trigger の channelId から対応ノードを O(1) で引きます。
        return this.nodeMap.get(id);
    }

    heatAncestors(node, amount) {
        // msg イベントでは対象チャンネルから root まで、祖先も含めて熱量を上げます。
        let current = node;
        let heat = amount;
        while (current) {
            // currentScore は今の見た目に直接効くため、即時に加算します。
            current.currentScore = Math.min(100, current.currentScore + heat);
            // targetScore は sync/decay の追従先なので、現在値より少し低めを最低値にします。
            current.targetScore = Math.max(
                current.targetScore,
                current.currentScore * 0.62,
            );
            // grand_root まで到達したら祖先探索を終了します。
            if (current.id === GRAND_ROOT_ID) break;
            // 親へ進み、次の祖先は熱量を弱めて加算します。
            current = this.nodeMap.get(current.parentId);
            heat *= 0.45;
        }
    }

    heatNode(node, amount) {
        // mov イベントでは移動先ノードだけを軽く光らせます。
        node.currentScore = Math.min(100, node.currentScore + amount);
        // targetScore も少し上げ、次フレーム以降も自然に残光するようにします。
        node.targetScore = Math.max(node.targetScore, node.currentScore * 0.7);
    }

    sync(deltas) {
        // backend から届いた channelId -> score の差分だけを反映します。
        for (const [id, score] of Object.entries(deltas ?? {})) {
            // payload に未知 ID が混ざっていても無視して描画を継続します。
            const node = this.nodeMap.get(id);
            if (node) {
                // currentScore は即座に変えず、update で targetScore へ滑らかに近づけます。
                node.targetScore = score;
            }
        }
    }

    update(delta, now) {
        // currentScore は見た目の明るさなので少し速く減衰させます。
        const decay = Math.exp(-delta * 0.78);
        // targetScore は backend 同期の追従先なので、current よりゆっくり減衰させます。
        const targetDecay = Math.exp(-delta * 0.48);
        for (const node of this.nodes) {
            // 経過時間に応じて現在値と目標値を減衰させます。
            node.currentScore *= decay;
            node.targetScore *= targetDecay;
            // currentScore を targetScore へ少しずつ寄せ、急なジャンプを避けます。
            node.currentScore += (node.targetScore - node.currentScore) * 0.07;
            // 極小値は 0 に丸め、描画のちらつきと不要な値変化を抑えます。
            if (node.currentScore < 0.015) node.currentScore = 0;
        }
        // 熱量に応じた揺れも含め、描画用の visualPosition を更新します。
        this.updateVisualPositions(now);
    }

    updateVisualPositions(now) {
        // performance.now はミリ秒なので、sin/cos 計算用に秒へ変換します。
        const time = now * 0.001;
        for (const node of this.nodes) {
            // 熱量が高いノードほど少し大きく漂うよう、振幅倍率を作ります。
            const heatLift = THREE.MathUtils.clamp(
                node.currentScore / 160,
                0,
                0.7,
            );
            // driftAmplitude は階層ごとの基本揺れ、heatLift はイベント時の上乗せです。
            const amplitude = node.driftAmplitude * (1 + heatLift);
            // driftAxisA 方向のゆっくりした正弦波です。
            const waveA =
                Math.sin(time * node.driftSpeed + node.driftPhase) * amplitude;
            // driftAxisB 方向は周期と位相をずらし、単調な往復運動に見えないようにします。
            const waveB =
                Math.cos(
                    time * node.driftSpeed * 0.73 + node.driftPhase * 1.31,
                ) *
                amplitude *
                0.58;
            // 基準位置に 2 軸の揺れを足したものを、実際の描画位置にします。
            node.visualPosition
                .copy(node.position)
                .addScaledVector(node.driftAxisA, waveA)
                .addScaledVector(node.driftAxisB, waveB);
        }
    }

    topNodes(limit) {
        // HUD の Heat パネル用に、grand_root 以外の熱量上位ノードを返します。
        return this.nodes
            .filter((node) => node.id !== GRAND_ROOT_ID)
            // currentScore の大きい順に並べます。
            .sort((a, b) => b.currentScore - a.currentScore)
            // UI に表示する件数だけに絞ります。
            .slice(0, limit)
            // Vue 側で扱いやすい plain object に変換します。
            .map((node) => ({
                id: node.id,
                name: node.name,
                score: node.currentScore,
                color: `#${node.color.getHexString()}`,
            }));
    }
}

function colorForChannel(channel) {
    // grand_root は島色ではなく、全体の中心として白寄りにします。
    if (channel.id === GRAND_ROOT_ID) {
        return new THREE.Color("#d6dde8");
    }
    // islandId ごとにベース色をパレットから選びます。
    const color = new THREE.Color(
        PALETTE[Math.max(0, channel.islandId ?? 0) % PALETTE.length],
    );
    // 深い階層ほど少し白へ寄せ、細かい枝が強すぎる色にならないようにします。
    const depthFade = THREE.MathUtils.clamp(
        1 - ((channel.depth ?? 0) - 1) * 0.055,
        0.58,
        1,
    );
    // depthFade が小さいほど白へ lerp します。
    return color.lerp(new THREE.Color("#dbe8ff"), 1 - depthFade);
}

function branchDistanceForDepth(depth) {
    // 階層が深くなるほど親子距離を短くし、島の外側で枝が密集しすぎないようにします。
    return 80 * Math.pow(0.3, depth - 1);
}

function spreadAngleForDepth(depth) {
    // root は大きく広げ、深い階層ほど狭い角度で枝分かれさせます。
    if (depth === 0) return Math.PI * 0.96;
    // 0.82 を掛け続けることで、深い枝が親方向から大きく外れすぎないようにします。
    return Math.PI * 0.76 * Math.pow(0.82, depth - 1);
}

function randomUnitVector() {
    // z を -1..1 から取り、球面上に偏りが少ないランダム方向を作ります。
    const z = THREE.MathUtils.randFloatSpread(2);
    // z に対応する水平半径を計算します。
    const radius = Math.sqrt(Math.max(0, 1 - z * z));
    // 水平方向の角度をランダムに選びます。
    const theta = Math.random() * Math.PI * 2;
    // 球面上の単位ベクトルとして返します。
    return new THREE.Vector3(
        Math.cos(theta) * radius,
        Math.sin(theta) * radius,
        z,
    );
}

function rotatedFibonacciDirections(count, jitter = 0) {
    // 子がいない場合は配置方向も不要です。
    if (count <= 0) return [];

    // Fibonacci sphere の方向を入れる配列です。
    const directions = [];
    // 毎回少し違う向きになるよう、全体にランダム回転をかけます。
    const rotation = new THREE.Quaternion().setFromEuler(
        new THREE.Euler(
            Math.random() * Math.PI,
            Math.random() * Math.PI,
            Math.random() * Math.PI,
        ),
    );

    for (let index = 0; index < count; index += 1) {
        // y を均等にずらし、球面上で上下に偏りにくい点を作ります。
        const y = 1 - ((index + 0.5) / count) * 2;
        // y に対応する水平半径を計算します。
        const radius = Math.sqrt(Math.max(0, 1 - y * y));
        // golden angle で角度を進め、点同士を均等に散らします。
        const theta = index * GOLDEN_ANGLE;
        // 回転前の Fibonacci sphere 方向です。
        const direction = new THREE.Vector3(
            Math.cos(theta) * radius,
            y,
            Math.sin(theta) * radius,
        );

        if (jitter > 0) {
            // 少しランダム方向を足し、完全な規則配置に見えないようにします。
            direction
                .addScaledVector(randomUnitVector(), Math.random() * jitter)
                .normalize();
        }

        // 全体ランダム回転をかけてから正規化し、最終方向として採用します。
        direction.applyQuaternion(rotation).normalize();
        directions.push(direction);
    }

    // root 直下の island 配置で使う方向配列を返します。
    return directions;
}
